package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func resultLinks(t *testing.T, result *mcp.CallToolResult) []string {
	t.Helper()
	require.False(t, result.IsError, "%+v", result.Content)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, string(structured), text.Text)
	links := make([]string, 0, len(result.Content)-1)
	for _, content := range result.Content[1:] {
		link, ok := content.(*mcp.ResourceLink)
		require.True(t, ok, "%T", content)
		assert.NotEmpty(t, link.Name)
		assert.Equal(t, "application/json", link.MIMEType)
		links = append(links, link.URI)
	}

	return links
}

func TestResultsLinkSessionResources(t *testing.T) {
	t.Parallel()
	id, listed := uuid.New(), uuid.New()
	boundary := &testBoundary{
		run: func(context.Context, control.ConversationRequest) (
			*control.ConversationResult, error,
		) {
			return &control.ConversationResult{SessionID: id}, nil
		},
		sessions: func(_ context.Context, p api.ListSessionsParams) (
			api.SessionPage, error,
		) {
			return api.SessionPage{
				Items: []api.Session{{Id: listed}}, Limit: *p.Limit,
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	base := "peen://session/" + id.String()
	sent := callTool(t, client, toolSendMessage,
		map[string]any{"workspace": "/workspace", "message": "hello"})
	assert.Equal(t, []string{base, base + "/messages"},
		resultLinks(t, sent))
	read := callTool(t, client, toolGetSession,
		map[string]any{"sessionId": id.String()})
	assert.Equal(t, []string{base, base + "/messages"},
		resultLinks(t, read))
	page := callTool(t, client, toolListSessions, map[string]any{})
	assert.Equal(t, []string{"peen://session/" + listed.String()},
		resultLinks(t, page))
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
