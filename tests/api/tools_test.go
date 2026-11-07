//go:build integration

package api_test

import (
	"encoding/json"
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

	hostToolsToolNameReadFile   = "readFile"
	hostToolsToolNameEditFile   = "editFile"
	hostToolsToolNameApplyPatch = "applyPatch"
	hostToolsToolNameRunCommand = "runCommand"

	// hostToolsMessageCount is one user message, three assistant/tool round
	// pairs, and the final assistant answer. The command finishes inside its
	// own runCommand call, so no job.exited event follows it: the tool
	// result already carries the outcome.
	hostToolsMessageCount = 8

	// hostToolsJobEventMarker is the framing every delivered event carries.
	hostToolsJobEventMarker = "<events>"
	// hostToolsJobExitedType is the event a finished job publishes.
	hostToolsJobExitedType = "job.exited"

	hostToolsPageLimit    = 3
	hostToolsCommandRanBy = "host-tools-command-ran"

	// hostToolsFixtureHeredoc is the quoted heredoc delimiter used to seed a
	// fixture file through the app container's own shell, as the app's own
	// runtime user, so a later editFile rename-replace lands on a file this
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

// hostToolsScenario is one scripted readFile/editFile/runCommand/answer
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

// runHostToolsScenario seeds the scenario's fixture file, scripts the
// provider mock to request readFile, editFile, and runCommand in order,
// drives one turn over the requested transport, and confirms runCommand
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
			// purpose is required: runCommand records why a job was started
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
		require.NoError(t, json.Unmarshal(
			[]byte(toolOutputText(t, messages[4].Content)), &output))
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
