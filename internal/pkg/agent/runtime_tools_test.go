package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	runtimeToolCallID    = "call_read"
	runtimeToolFileName  = "note.txt"
	runtimeToolFileBody  = "alpha\nbeta\n"
	runtimeToolFinalText = "read it"

	runtimeToolSecretFileName = "secret.txt"
	runtimeToolSecretBody     = "test-private-content-redaction-marker"
)

func runtimeToolArguments(t *testing.T, path string) string {
	t.Helper()

	encoded, err := json.Marshal(tools.ReadFileInput{Path: path})
	require.NoError(t, err)

	return string(encoded)
}

func collectEvents(events *[]Event) EventSink {
	return func(event Event) error {
		*events = append(*events, event)

		return nil
	}
}

func decodeEventPayload(t *testing.T, events []Event, eventType string) []byte {
	t.Helper()

	for _, event := range events {
		if event.Type == eventType {
			return event.Payload
		}
	}

	require.FailNowf(t, "event not found", "no %s event", eventType)

	return nil
}

// One scripted tool round must produce a paired tool_use and tool_result on the
// event stream, a durable tool-role message, and durable events, all sharing
// Elelem's call ID.
func TestRuntimeRunExecutesHostToolAndPairsCallID(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName),
		),
		elelemtest.Text(runtimeToolFinalText),
	))
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, runtimeToolFileName),
		runtimeToolFileBody,
	)

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the note",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)

	assert.Equal(
		t,
		[]string{
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	use := toolUsePayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolUse),
		&use,
	))
	assert.Equal(t, runtimeToolCallID, use.CallID)
	assert.Equal(t, toolNameReadFile, use.Name)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.Equal(t, runtimeToolCallID, toolResult.CallID)
	assert.False(t, toolResult.IsError)

	output := tools.ReadFileOutput{}
	require.NoError(t, json.Unmarshal([]byte(toolResult.Content), &output))
	assert.Equal(t, runtimeToolFileBody, output.Content)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleAssistant,
			models.MessageRoleTool,
			models.MessageRoleAssistant,
		},
		messageRoles(messages.Items),
	)

	toolMessage := messages.Items[2]
	assert.Equal(t, runtimeToolCallID, toolMessage.ToolCallID)
	assert.False(t, toolMessage.IsError)
	assert.Contains(
		t,
		messages.Items[1].ToolCallsJSON,
		runtimeToolCallID,
		"the assistant message must record the call it requested",
	)
}

// A failing tool is a result the model can react to, not a failed turn.
func TestRuntimeRunKeepsFailingToolVisibleToTheModel(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameReadFile,
			runtimeToolArguments(t, "absent.txt"),
		),
		elelemtest.Text(runtimeToolFinalText),
	))

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the missing note",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "a tool failure must not fail the turn")
	assert.Equal(t, runtimeToolFinalText, result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.Equal(t, runtimeToolCallID, toolResult.CallID)
	assert.True(t, toolResult.IsError)
	assert.NotContains(
		t,
		toolResult.Content,
		errorLocationMarker,
		"model-visible errors must not carry source locations",
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.True(
		t,
		messages.Items[2].IsError,
		"the durable tool message must record the failure",
	)
}

// Tool observability logs must carry metadata, not file contents. This
// replaces the process-global slog default, so it must not run in parallel
// with anything else that reads or sets it.
func TestRuntimeRunToolLogsCarryNoFileContents(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameReadFile,
			runtimeToolArguments(t, runtimeToolSecretFileName),
		),
		elelemtest.Text(runtimeToolFinalText),
	))
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, runtimeToolSecretFileName),
		runtimeToolSecretBody,
	)

	var captured bytes.Buffer

	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the secret",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)

	// The tool result event and durable transcript legitimately carry the
	// file body verbatim; only the operational log line must not.
	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))

	assert.Contains(t, toolResult.Content, runtimeToolSecretBody)

	logOutput := captured.String()
	assert.Contains(t, logOutput, "host tool call completed")
	assert.Contains(t, logOutput, toolNameReadFile)
	assert.Contains(t, logOutput, runtimeToolCallID)
	assert.NotContains(t, logOutput, runtimeToolSecretBody)
}
