package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	queuedUserMessageInitial = "inspect the workspace"
	queuedUserMessageText    = "also inspect the tests"
	queuedUserMessageFinal   = "queued message handled"
)

func TestRuntimeQueuesUserMessageAtActiveTurnRoundBoundary(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) { options.MaxConcurrentTurns = 1 },
	)

	var (
		sessionID uuid.UUID
		queued    *TurnResult
		queueErr  error
	)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			switch event.Type {
			case EventTypeTurnStarted:
				started := turnStartedPayload{}
				if err := json.Unmarshal(event.Payload, &started); err != nil {
					return err
				}

				parsed, err := uuid.Parse(started.SessionID)
				if err != nil {
					return err
				}

				sessionID = parsed
			case EventTypeToolUse:
				queued, queueErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{
						SessionID: &sessionID,
						Message:   queuedUserMessageText,
					},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.True(t, queued.Queued)
	assert.Equal(t, result.SessionID, queued.SessionID)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, elelem.RoleUser, requests[1].Messages[len(requests[1].Messages)-1].Role)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())

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
			models.MessageRoleUser,
			models.MessageRoleAssistant,
		},
		messageRoles(messages.Items),
	)
	assert.Equal(t, queuedUserMessageText, messages.Items[3].Content)

	query := repositories.Use(fixture.handle.GormDB)
	auditEvents, err := query.Event.WithContext(context.Background()).
		Where(query.Event.SessionID.Eq(result.SessionID)).
		Where(query.Event.EventType.Eq(EventTypeUserMessageQueued)).
		Find()
	require.NoError(t, err)
	require.Len(t, auditEvents, 1)

	payload := queuedUserMessagePayload{}
	require.NoError(t, json.Unmarshal([]byte(auditEvents[0].PayloadJSON), &payload))
	assert.Equal(t, queuedUserMessageText, payload.Message)
}

func TestRuntimeStreamMessageQueuesActiveTurn(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixture(t, driver)

	var (
		sessionID uuid.UUID
		queued    *StreamMessageResult
		queueErr  error
	)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			switch event.Type {
			case EventTypeTurnStarted:
				started := turnStartedPayload{}
				if err := json.Unmarshal(event.Payload, &started); err != nil {
					return err
				}

				parsed, err := uuid.Parse(started.SessionID)
				if err != nil {
					return err
				}

				sessionID = parsed
			case EventTypeToolUse:
				queued, queueErr = fixture.runtime.StreamMessage(
					context.Background(),
					api.MessageRequest{Message: queuedUserMessageText},
					&sessionID,
					uuid.New(),
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.Nil(t, queued.Body)
	assert.True(t, queued.Queued)
	assert.Equal(t, result.SessionID, queued.SessionID)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())
}

func TestRuntimeRejectsFullActiveUserMessageQueue(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) { options.MaxQueuedUserMessages = 1 },
	)

	var (
		sessionID uuid.UUID
		first     *TurnResult
		firstErr  error
		secondErr error
	)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			switch event.Type {
			case EventTypeTurnStarted:
				started := turnStartedPayload{}
				if err := json.Unmarshal(event.Payload, &started); err != nil {
					return err
				}

				parsed, err := uuid.Parse(started.SessionID)
				if err != nil {
					return err
				}

				sessionID = parsed
			case EventTypeToolUse:
				first, firstErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{SessionID: &sessionID, Message: queuedUserMessageText},
				)
				_, secondErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{SessionID: &sessionID, Message: "one message too many"},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, firstErr)
	require.NotNil(t, first)
	assert.True(t, first.Queued)
	require.Error(t, secondErr)
	assert.ErrorIs(t, secondErr, commerr.ErrConflict)
	assert.ErrorIs(t, secondErr, elelem.ErrUserMessageQueueFull)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())
}

func TestRuntimeRejectsActiveTurnOverrides(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	))

	var (
		sessionID uuid.UUID
		queueErr  error
	)
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			switch event.Type {
			case EventTypeTurnStarted:
				started := turnStartedPayload{}
				if err := json.Unmarshal(event.Payload, &started); err != nil {
					return err
				}

				parsed, err := uuid.Parse(started.SessionID)
				if err != nil {
					return err
				}

				sessionID = parsed
			case EventTypeToolUse:
				_, queueErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{
						SessionID: &sessionID,
						Message:   queuedUserMessageText,
						Workspace: fixture.otherWorkspace,
					},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.Error(t, queueErr)
	assert.ErrorIs(t, queueErr, commerr.ErrConflict)
	assert.False(t, errors.Is(queueErr, elelem.ErrUserMessageQueueFull))
}
