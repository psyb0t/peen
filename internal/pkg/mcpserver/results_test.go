package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExistingSessionMapping(t *testing.T) {
	t.Parallel()
	requests := make(chan control.ConversationRequest, 1)
	guards := make(chan uuid.UUID, 1)
	id := uuid.New()
	boundary := &testBoundary{
		guard: func(_ context.Context, selected uuid.UUID) error {
			guards <- selected

			return nil
		},
		run: func(_ context.Context, request control.ConversationRequest) (
			*control.ConversationResult, error,
		) {
			requests <- request

			return &control.ConversationResult{SessionID: id}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	decodeResult[sendResult](t, callTool(t, client, toolSendMessage,
		map[string]any{"sessionId": id.String(), "message": "hello"}))
	assert.Equal(t, id, <-guards)
	request := <-requests
	assert.Equal(t, id, request.SessionID)
	assert.Empty(t, request.Workspace)
	assert.Empty(t, request.Message.OutputSchema)
	assert.Zero(t, request.Message.TimeoutSeconds)
}

func TestInvalidOutputAndSchemaFailure(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		output      json.RawMessage
		outputError string
		failed      bool
	}{
		{name: "invalid JSON", output: json.RawMessage(`broken`), failed: true},
		{name: "schema failure", outputError: "missing property \"answer\""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			boundary := &testBoundary{run: func(
				context.Context, control.ConversationRequest,
			) (*control.ConversationResult, error) {
				return &control.ConversationResult{
					SessionID: uuid.New(),
					Output:    tc.output, OutputError: tc.outputError,
				}, nil
			}}
			client := testClient(t, boundary.dependencies())
			result := callTool(t, client, toolSendMessage,
				map[string]any{"workspace": "/workspace", "message": "hello"})
			if tc.failed {
				assert.True(t, result.IsError)

				return
			}
			answer := decodeResult[sendResult](t, result)
			assert.Empty(t, answer.Output)
			assert.Equal(t, tc.outputError, answer.OutputError)
		})
	}
}

func TestUnexpectedSessionGuardFailure(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{
		guard: func(context.Context, uuid.UUID) error {
			return commerr.ErrInvalidState
		},
		sessions: func(context.Context, api.ListSessionsParams) (
			api.SessionPage, error,
		) {
			return api.SessionPage{Items: []api.Session{{Id: uuid.New()}}}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	result := callTool(t, client, toolListSessions, map[string]any{})
	require.True(t, result.IsError)
	assert.Equal(t, failureBusy, failureMessage(commerr.ErrLockHeld))
}
