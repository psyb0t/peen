package mcpserver

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendMessageMapping(t *testing.T) {
	t.Parallel()
	requests := make(chan control.ConversationRequest, 1)
	id := uuid.New()
	boundary := &testBoundary{run: func(
		_ context.Context, request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		requests <- request

		return &control.ConversationResult{
			SessionID: id, Output: json.RawMessage(`{"ok":true}`),
		}, nil
	}}
	client := testClient(t, boundary.dependencies())
	answer := decodeResult[sendResult](t, callTool(t, client, toolSendMessage,
		map[string]any{
			"workspace": "/workspace", "message": "hello",
			"agentSpace": "default", "model": "example/model",
			"reasoningEffort": "high", "timeoutSeconds": 12,
			"outputSchema": map[string]any{},
		},
	))
	assert.Equal(t, id.String(), answer.SessionID)
	assert.JSONEq(t, `{"ok":true}`, string(answer.Output))
	request := <-requests
	assert.Equal(t, "/workspace", request.Workspace)
	assert.Equal(t, "default", request.AgentSpace)
	assert.False(t, request.Message.System)
	assert.Nil(t, request.Message.To)
	assert.Nil(t, request.Message.Origin)
	assert.Nil(t, request.Message.SystemPrompt)
	assert.Equal(t, "example/model", *request.Message.Model)
	assert.Equal(t, "high", *request.Message.ReasoningEffort)
	assert.Equal(t, 12, request.Message.TimeoutSeconds)
	assert.JSONEq(t, `{}`, string(request.Message.OutputSchema))
}

func TestSendMessageValidation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		args map[string]any
	}{
		{"no selector", map[string]any{"message": "hello"}},
		{"two selectors", map[string]any{
			"sessionId": uuid.NewString(),
			"workspace": "/workspace", "message": "hello",
		}},
		{"malformed id", map[string]any{"sessionId": "bad", "message": "hi"}},
		{"model", map[string]any{"model": "missing"}},
		{"effort", map[string]any{"reasoningEffort": "unbounded"}},
		{"zero timeout", map[string]any{"timeoutSeconds": 0}},
		{"large timeout", map[string]any{"timeoutSeconds": 86401}},
		{"schema", map[string]any{"outputSchema": map[string]any{
			"type": "imaginary",
		}}},
		{"schema ref", map[string]any{"outputSchema": map[string]any{
			"$ref": "https://example.invalid/schema",
		}}},
		{"system", map[string]any{"system": true}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{
				"workspace": "/workspace", "message": "hello",
			}
			if tc.name == "no selector" || tc.name == "malformed id" {
				delete(args, "workspace")
			}
			maps.Copy(args, tc.args)
			client := testClient(t, (&testBoundary{}).dependencies())
			assert.True(t, callTool(t, client, toolSendMessage, args).IsError)
		})
	}
}

func TestSendMessageCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	stopped := make(chan struct{})
	boundary := &testBoundary{run: func(
		ctx context.Context, _ control.ConversationRequest,
	) (*control.ConversationResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)

		return nil, ctx.Err()
	}}
	client := testClient(t, boundary.dependencies())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, &mcp.CallToolParams{
			Name: toolSendMessage, Arguments: map[string]any{
				"workspace": "/workspace", "message": "hello",
			},
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("client call did not stop after cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("runner context was not cancelled")
	}
}

func TestSendMessageReportsAnAgentSpaceConflict(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{run: func(
		context.Context, control.ConversationRequest,
	) (*control.ConversationResult, error) {
		return nil, ctxerrors.Wrap(&session.AgentSpaceConflictError{
			Current: "docker:sandbox", Requested: "control",
		}, "private open conversation workspace")
	}}
	client := testClient(t, boundary.dependencies())
	result := callTool(t, client, toolSendMessage, map[string]any{
		"workspace": "/workspace", "message": "hello", "agentSpace": "control",
	})
	require.True(t, result.IsError)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t,
		`this workspace runs in agent space "docker:sandbox", not "control"`,
		text.Text)
	assert.NotEqual(t, failureBusy, text.Text)
}
