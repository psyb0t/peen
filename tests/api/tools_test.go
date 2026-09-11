//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	hostToolsUserMessage = "run the scripted host tools sequence"

	hostToolsToolNameReadFile   = "read_file"
	hostToolsToolNameEditFile   = "edit_file"
	hostToolsToolNameApplyPatch = "apply_patch"
	hostToolsToolNameRunCommand = "run_command"

	// hostToolsMessageCount is one user message, three assistant/tool round
	// pairs, the injected job.exited event, and the final assistant answer.
	//
	// The event message is not incidental: run_command starts a supervised
	// job, the job publishes job.exited when it finishes, and the next tool
	// boundary delivers that to the model as a durable user message. Its
	// presence here is the proof that the event bus and the job registry are
	// actually wired to each other through a real turn.
	hostToolsMessageCount = 9

	// hostToolsJobEventMarker is the framing every delivered event carries.
	hostToolsJobEventMarker = "<session-events"
	// hostToolsJobExitedType is the event a finished job publishes.
	hostToolsJobExitedType = "job.exited"

	hostToolsPageLimit    = 3
	hostToolsCommandRanBy = "host-tools-command-ran"

	// hostToolsFixtureHeredoc is the quoted heredoc delimiter used to seed a
	// fixture file through the app container's own shell, as the app's own
	// runtime user, so a later edit_file rename-replace lands on a file this
	// process already owns rather than one dropped in by CopyToContainer
	// (which extracts as root regardless of the container's configured
	// user).
	hostToolsFixtureHeredoc = "HOST_TOOLS_FIXTURE_EOF"

	hostToolsFixtureFileJSON = "host-tools-json-fixture.txt"
	hostToolsContentJSON     = "alpha\nTOOLS_JSON_MARKER\nomega\n"
	hostToolsEditedJSON      = "alpha\nTOOLS_JSON_REPLACED\nomega\n"
	hostToolsEditOldJSON     = "TOOLS_JSON_MARKER"
	hostToolsEditNewJSON     = "TOOLS_JSON_REPLACED"
	hostToolsCommandFileJSON = "host-tools-json-command-output.txt"
	hostToolsFinalAnswerJSON = "host tools turn complete"

	hostToolsFixtureFilePatch = "host-tools-patch-fixture.txt"
	hostToolsContentPatch     = "alpha\nTOOLS_PATCH_MARKER\nomega\n"
	hostToolsEditedPatch      = "alpha\nTOOLS_PATCH_REPLACED\nomega\n"
	hostToolsEditOldPatch     = "TOOLS_PATCH_MARKER"
	hostToolsEditNewPatch     = "TOOLS_PATCH_REPLACED"
	hostToolsCommandFilePatch = "host-tools-patch-command-output.txt"

	hostToolsFixtureFileFail = "host-tools-fail-fixture.txt"
	hostToolsContentFail     = "alpha\nTOOLS_FAIL_MARKER\nomega\n"
	hostToolsEditOldFail     = "TOOLS_FAIL_TEXT_NOT_PRESENT"
	hostToolsEditNewFail     = "TOOLS_FAIL_REPLACED"
	hostToolsCommandFileFail = "host-tools-fail-command-output.txt"
	hostToolsFinalAnswerFail = "host tools turn complete despite a failed edit"
)

// hostToolsScenario is one scripted read_file/edit_file/run_command/answer
// conversation, driven against its own fixture file so successful and
// failing-edit runs never touch each other's state.
type hostToolsScenario struct {
	fixtureFileName    string
	fixtureContent     string
	editOld            string
	editNew            string
	editIsError        bool
	useApplyPatch      bool
	wantFixtureContent string
	commandFileName    string
	finalAnswer        string
}

// hostToolsResult is what a scenario run leaves behind for its caller to
// assert on: the final answer text and the session it ran in.
type hostToolsResult struct {
	finalText string
	sessionID uuid.UUID
}

func TestAPIHostToolsProductionWiring(t *testing.T) {
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	jsonResult := runHostToolsScenario(t, hostToolsScenario{
		fixtureFileName:    hostToolsFixtureFileJSON,
		fixtureContent:     hostToolsContentJSON,
		editOld:            hostToolsEditOldJSON,
		editNew:            hostToolsEditNewJSON,
		wantFixtureContent: hostToolsEditedJSON,
		commandFileName:    hostToolsCommandFileJSON,
		finalAnswer:        hostToolsFinalAnswerJSON,
	})

	runHostToolsScenario(t, hostToolsScenario{
		fixtureFileName:    hostToolsFixtureFilePatch,
		fixtureContent:     hostToolsContentPatch,
		editOld:            hostToolsEditOldPatch,
		editNew:            hostToolsEditNewPatch,
		wantFixtureContent: hostToolsEditedPatch,
		commandFileName:    hostToolsCommandFilePatch,
		finalAnswer:        hostToolsFinalAnswerJSON,
		useApplyPatch:      true,
	})

	assert.NotEmpty(t, jsonResult.finalText)

	runHostToolsScenario(t, hostToolsScenario{
		fixtureFileName:    hostToolsFixtureFileFail,
		fixtureContent:     hostToolsContentFail,
		editOld:            hostToolsEditOldFail,
		editNew:            hostToolsEditNewFail,
		editIsError:        true,
		wantFixtureContent: hostToolsContentFail,
		commandFileName:    hostToolsCommandFileFail,
		finalAnswer:        hostToolsFinalAnswerFail,
	})
}

// runHostToolsScenario seeds the scenario's fixture file, scripts the
// provider mock to request read_file, edit_file, and run_command in order,
// drives one turn over the requested transport, and confirms run_command
// actually touched the workspace before returning.
func runHostToolsScenario(
	t *testing.T,
	scenario hostToolsScenario,
) hostToolsResult {
	t.Helper()

	fixturePath := hostToolsContainerPath(scenario.fixtureFileName)
	commandPath := hostToolsContainerPath(scenario.commandFileName)
	seedContainerFile(t, fixturePath, scenario.fixtureContent)

	scriptedTurn := testinfra.ScriptedToolTurn{
		ReadFileArguments: map[string]any{
			"path": fixturePath,
		},
		EditFileArguments: map[string]any{
			"path": fixturePath,
			"edits": []map[string]any{{
				"old": scenario.editOld,
				"new": scenario.editNew,
			}},
		},
		RunCommandArguments: map[string]any{
			"command": "printf '" + hostToolsCommandRanBy + "' > " + commandPath,
			// purpose is required: run_command records why a job was started
			// so a later turn can tell what a running process is for.
			"purpose": "write the marker file this test checks for",
		},
		FinalAnswer: scenario.finalAnswer,
	}
	if scenario.useApplyPatch {
		scriptedTurn.EditFileArguments = nil
		scriptedTurn.ApplyPatchArguments = map[string]any{
			"patch": strings.Join([]string{
				"*** Begin Patch",
				"*** Update File: " + fixturePath,
				"@@",
				" alpha",
				"-" + scenario.editOld,
				"+" + scenario.editNew,
				" omega",
				"*** End Patch",
			}, "\n"),
		}
	}
	integrationInfra.EnableScriptedToolTurn(scriptedTurn)

	result := runHostToolsTurn(t)

	assert.Equal(
		t,
		hostToolsCommandRanBy,
		readContainerFile(t, commandPath),
	)
	assertHostToolsFixture(
		t, scenario.fixtureFileName, scenario.wantFixtureContent,
	)
	assertHostToolsTranscript(
		t,
		result.sessionID,
		scenario.editIsError,
		scenario.useApplyPatch,
		result.finalText,
	)

	return result
}

func runHostToolsTurn(t *testing.T) hostToolsResult {
	t.Helper()

	sessionID := uuid.New()
	result := sendAPIWebSocketMessage(t, sessionID, hostToolsUserMessage)
	require.False(t, result.result.Queued)
	messages := collectAllMessages(t, sessionID)
	require.NotEmpty(t, messages)

	return hostToolsResult{
		finalText: messages[len(messages)-1].Content,
		sessionID: sessionID,
	}
}

// hostToolsContainerPath places a scenario fixture under the same directory
// the app treats as its default tool workspace (PEEN_WORKING_DIR), so a
// relative read_file/edit_file/run_command path would resolve here too.
func hostToolsContainerPath(name string) string {
	return path.Join(testinfra.ContainerWorkingDirectory, name)
}

// seedContainerFile writes content to containerPath through the running
// app container's own shell, via a quoted heredoc so no shell metacharacter
// in content is ever interpreted. That makes the file's owner the app's own
// runtime user, matching a file the app wrote itself.
func seedContainerFile(t *testing.T, containerPath, content string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	script := "cat > " + shellSingleQuote(containerPath) + " <<'" +
		hostToolsFixtureHeredoc + "'\n" + content + hostToolsFixtureHeredoc + "\n"

	exitCode, output, err := integrationInfra.App.Exec(
		ctx,
		[]string{"sh", "-c", script},
	)
	require.NoError(t, err)

	outputBytes, readErr := io.ReadAll(output)
	require.NoError(t, readErr)
	require.Equal(t, 0, exitCode, "seed fixture file: %s", outputBytes)
}

// shellSingleQuote wraps value for safe use as one POSIX shell word.
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func readContainerFile(t *testing.T, containerPath string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reader, err := integrationInfra.App.CopyFileFromContainer(ctx, containerPath)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, reader.Close())
	}()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(data)
}

func assertHostToolsFixture(t *testing.T, fileName, wantContent string) {
	t.Helper()

	got := readContainerFile(t, hostToolsContainerPath(fileName))
	assert.Equal(t, wantContent, got)
}

// assertHostToolsTranscript walks every page of the session's durable
// messages and checks the fixed eight-message shape: the user message, one
// assistant/tool pair per scripted tool call carrying the call's id, and the
// final assistant answer.
func assertHostToolsTranscript(
	t *testing.T,
	sessionID uuid.UUID,
	editIsError bool,
	useApplyPatch bool,
	finalAnswer string,
) {
	t.Helper()

	messages := collectAllMessages(t, sessionID)
	require.Len(t, messages, hostToolsMessageCount)

	assert.Equal(t, api.MessageRoleUser, messages[0].Role)
	assert.Equal(t, hostToolsUserMessage, messages[0].Content)

	assertToolRound(
		t, messages[1], messages[2],
		hostToolsToolNameReadFile, testinfra.ScriptedCallIDReadFile, false,
	)
	mutationToolName := hostToolsToolNameEditFile
	mutationCallID := testinfra.ScriptedCallIDEditFile
	if useApplyPatch {
		mutationToolName = hostToolsToolNameApplyPatch
		mutationCallID = testinfra.ScriptedCallIDApplyPatch
	}
	assertToolRound(
		t,
		messages[3],
		messages[4],
		mutationToolName,
		mutationCallID,
		editIsError,
	)
	if useApplyPatch {
		var output struct {
			Files []json.RawMessage `json:"files"`
		}
		require.NoError(t, json.Unmarshal([]byte(messages[4].Content), &output))
		require.Len(t, output.Files, 1)
	}
	assertToolRound(
		t, messages[5], messages[6],
		hostToolsToolNameRunCommand, testinfra.ScriptedCallIDRunCommand, false,
	)

	// The job started by run_command finished, published job.exited, and the
	// next tool boundary delivered it as quoted data.
	delivered := messages[7]
	assert.Equal(t, api.MessageRoleUser, delivered.Role)
	assert.Contains(t, delivered.Content, hostToolsJobEventMarker)
	assert.Contains(t, delivered.Content, hostToolsJobExitedType)

	final := messages[8]
	assert.Equal(t, api.MessageRoleAssistant, final.Role)
	assert.Equal(t, finalAnswer, final.Content)
}

// assertToolRound checks one assistant-requests-a-tool / tool-answers pair:
// the assistant message names and IDs the call, and the tool message carries
// the same call ID plus the expected error flag.
func assertToolRound(
	t *testing.T,
	assistantMessage api.Message,
	toolMessage api.Message,
	toolName string,
	callID string,
	wantError bool,
) {
	t.Helper()

	assert.Equal(t, api.MessageRoleAssistant, assistantMessage.Role)
	require.NotNil(t, assistantMessage.ToolCalls)
	require.Len(t, *assistantMessage.ToolCalls, 1)
	assert.Equal(t, callID, (*assistantMessage.ToolCalls)[0].Id)
	assert.Equal(t, toolName, (*assistantMessage.ToolCalls)[0].Name)

	assert.Equal(t, api.MessageRoleTool, toolMessage.Role)
	require.NotNil(t, toolMessage.ToolCallId)
	assert.Equal(t, callID, *toolMessage.ToolCallId)

	// IsError is only carried on the wire when true; a successful tool
	// result omits it entirely rather than sending an explicit false.
	gotError := toolMessage.IsError != nil && *toolMessage.IsError
	assert.Equal(t, wantError, gotError)
}

// collectAllMessages walks every page of a session's message list with a
// page size smaller than the total, rather than trusting page one alone.
func collectAllMessages(t *testing.T, sessionID uuid.UUID) []api.Message {
	t.Helper()

	messages := make([]api.Message, 0, hostToolsMessageCount)
	offset := 0

	for {
		page := listMessages(t, sessionID, hostToolsPageLimit, offset, "asc")
		messages = append(messages, page.Items...)
		offset += len(page.Items)

		if !page.HasMore {
			break
		}
	}

	return messages
}
