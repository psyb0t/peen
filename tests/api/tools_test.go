//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

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
	// pairs, and the final assistant answer. The command finishes inside its
	// own run_command call, so no job.exited event follows it: the tool
	// result already carries the outcome.
	hostToolsMessageCount = 8

	// hostToolsJobEventMarker is the framing every delivered event carries.
	hostToolsJobEventMarker = "<session-events"
	// hostToolsJobExitedType is the event a finished job publishes.
	hostToolsJobExitedType = "job.exited"

	sessionNoticesPath = sessionPath + "/notices"

	backgroundJobUserMessage    = "run the scripted background job sequence"
	backgroundJobFollowUp       = "what finished while you were away"
	backgroundJobFixtureFile    = "background-job-fixture.txt"
	backgroundJobFixtureContent = "background job fixture\n"
	backgroundJobFixtureEdited  = "background job fixture edited\n"
	backgroundJobCommand        = "sleep 2 && echo background job done"
	backgroundJobPurpose        = "outlive the one second wait"
	backgroundJobWaitSeconds    = 1
	backgroundJobFinalAnswer    = "background job started"
	backgroundJobEventWait      = 15 * time.Second
	backgroundJobEventPoll      = 200 * time.Millisecond

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
	messages  []api.Message
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

// A command still running when its run_command call returns finishes as a
// background job. Its job.exited event goes through the real job registry and
// event bus, is stored as a session notice, and opens the next turn as an
// injected update.
func TestAPIBackgroundJobPublishesItsCompletion(t *testing.T) {
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	fixturePath := hostToolsContainerPath(backgroundJobFixtureFile)
	seedContainerFile(t, fixturePath, backgroundJobFixtureContent)

	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage:       backgroundJobUserMessage,
		ReadFileArguments: map[string]any{"path": fixturePath},
		EditFileArguments: map[string]any{
			"path": fixturePath,
			"edits": []map[string]any{{
				"old": backgroundJobFixtureContent,
				"new": backgroundJobFixtureEdited,
			}},
		},
		RunCommandArguments: map[string]any{
			"command":        backgroundJobCommand,
			"purpose":        backgroundJobPurpose,
			"timeoutSeconds": backgroundJobWaitSeconds,
		},
		FinalAnswer: backgroundJobFinalAnswer,
	})

	result := sendAPIWebSocketMessage(t, backgroundJobUserMessage)
	integrationInfra.DisableScriptedToolTurn()

	require.Eventually(t, func() bool {
		return hasNoticeOfType(t, result.sessionID, hostToolsJobExitedType)
	}, backgroundJobEventWait, backgroundJobEventPoll,
		"the background job published job.exited")

	sendAPIWebSocketMessage(t, backgroundJobFollowUp)

	messages := collectAllMessages(t, result.sessionID)
	delivered := false
	for _, message := range messages {
		if message.Injected == nil || !*message.Injected {
			continue
		}
		if strings.Contains(message.Content, hostToolsJobEventMarker) &&
			strings.Contains(message.Content, hostToolsJobExitedType) {
			delivered = true
		}
	}
	assert.True(t, delivered, "the next turn opened with the job.exited update")
}

func hasNoticeOfType(t *testing.T, sessionID uuid.UUID, noticeType string) bool {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodGet,
		sessionNoticesPath,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	page := decodeResponse[api.SessionNoticePage](t, response)
	for _, notice := range page.Notices {
		if notice.Type == noticeType {
			return true
		}
	}

	return false
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
		UserMessage: hostToolsUserMessage,
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
		result.messages,
		scenario.editIsError,
		scenario.useApplyPatch,
		result.finalText,
	)

	return result
}

func runHostToolsTurn(t *testing.T) hostToolsResult {
	t.Helper()

	result := sendAPIWebSocketMessage(t, hostToolsUserMessage)
	require.False(t, result.result.Queued)
	messages := collectAllMessages(t, result.sessionID)
	require.GreaterOrEqual(t, len(messages), hostToolsMessageCount)
	turnMessages := messages[len(messages)-hostToolsMessageCount:]

	return hostToolsResult{
		finalText: turnMessages[len(turnMessages)-1].Content,
		sessionID: result.sessionID,
		messages:  turnMessages,
	}
}

// hostToolsContainerPath places a scenario fixture under the app's immutable
// tool workspace, so a relative tool path would resolve here too.
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
	messages []api.Message,
	editIsError bool,
	useApplyPatch bool,
	finalAnswer string,
) {
	t.Helper()

	require.Len(t, messages, hostToolsMessageCount)

	assert.Equal(t, api.MessageRoleUser, messages[0].Role)
	assert.Equal(t, hostToolsUserMessage, messages[0].Content)
	assert.Nil(t, messages[0].Injected, "a typed prompt is not marked as injected")

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

	// The command finished inside its call, so the model was not told the
	// same result a second time as a session event.
	for _, message := range messages {
		assert.NotContains(t, message.Content, hostToolsJobEventMarker)
	}

	final := messages[7]
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
