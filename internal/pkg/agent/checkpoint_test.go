package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	checkpointCallID    = "checkpoint_tool_call"
	checkpointFileName  = "checkpoint.txt"
	checkpointFileBody  = "checkpoint fixture body"
	checkpointFinalText = "checkpoint turn finished"
)

// A process killed mid-turn used to lose every assistant message and tool call
// the turn had produced, because nothing was written until the turn ended. The
// tool.result event is published only after the checkpoint, so observing the
// store from the sink proves the rows are durable while the turn is still
// running, which is exactly what a crash at that moment would keep.
func TestRuntimeCheckpointsToolProgressBeforeItPublishes(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			checkpointCallID,
			toolNameReadFile,
			runtimeToolArguments(t, checkpointFileName),
		),
		elelemtest.Text(checkpointFinalText),
	))

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, checkpointFileName),
		checkpointFileBody,
	)

	var (
		sessionID uuid.UUID
		durable   []*models.Message
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the fixture",
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			switch event.Type {
			case EventTypeTurnStarted:
				sessionID = sessionIDFromTurnStarted(t, event)
			case EventTypeToolResult:
				durable = listMessages(t, fixture, sessionID)
			}

			return nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, checkpointFinalText, result.Text)

	roles := messageRoles(durable)
	assert.Contains(
		t,
		roles,
		models.MessageRoleAssistant,
		"the assistant tool call must be durable before the result is sent",
	)
	assert.Contains(
		t,
		roles,
		models.MessageRoleTool,
		"the tool result must be durable before the result is sent",
	)
}

// Checkpointed rows must not be written a second time when the turn ends.
func TestRuntimeCheckpointDoesNotDuplicateOnFinalization(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			checkpointCallID,
			toolNameReadFile,
			runtimeToolArguments(t, checkpointFileName),
		),
		elelemtest.Text(checkpointFinalText),
	))

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, checkpointFileName),
		checkpointFileBody,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the fixture",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	messages := listMessages(t, fixture, result.SessionID)

	// One user message, the assistant tool call, its tool result, and the
	// final assistant answer. Any duplicate means the tail bookkeeping
	// rewrote what a checkpoint had already committed.
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleAssistant,
			models.MessageRoleTool,
			models.MessageRoleAssistant,
		},
		messageRoles(messages),
	)
}

func sessionIDFromTurnStarted(t *testing.T, event Event) uuid.UUID {
	t.Helper()

	payload := turnStartedPayload{}
	require.NoError(t, json.Unmarshal(event.Payload, &payload))

	parsed, err := uuid.Parse(payload.SessionID)
	require.NoError(t, err)

	return parsed
}

func listMessages(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) []*models.Message {
	t.Helper()

	page, err := fixture.store.ListMessages(
		context.Background(),
		sessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	return page.Items
}
