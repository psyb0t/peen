package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolCatalogue(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	result, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 10)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		assert.NotEmpty(t, tool.Description)
		encoded, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var definition struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(encoded, &definition))
		for name, property := range definition.Properties {
			assert.NotEmpty(t, property.Description, "%s.%s", tool.Name, name)
		}
	}
	assert.ElementsMatch(t, []string{
		toolSendMessage, toolListSessions,
		toolGetSession, toolReadMessages, toolCancelTurn, toolListModels,
		toolListFiles, toolReadFile, toolWriteFile, toolDeleteFile,
	}, names)
}

func TestSystemSessionsRefused(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{guard: func(context.Context, uuid.UUID) error {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"private internal path /private/worker")
	}}
	client := testClient(t, boundary.dependencies())
	for _, name := range []string{
		toolSendMessage, toolGetSession,
		toolReadMessages, toolCancelTurn, toolListFiles, toolReadFile,
		toolWriteFile, toolDeleteFile,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"sessionId": uuid.NewString()}
			switch name {
			case toolSendMessage:
				args["message"] = "hello"
			case toolReadFile, toolDeleteFile:
				args["path"] = "file.txt"
			case toolWriteFile:
				args["path"], args["contentBase64"] = "file.txt", "aGk="
			}
			result := callTool(t, client, name, args)
			require.True(t, result.IsError)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			assert.Equal(t, failureDenied, text.Text)
			assert.Zero(t, boundary.accesses.Load())
		})
	}
}

func TestSystemSessionResourcesRefused(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{guard: func(context.Context, uuid.UUID) error {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"private internal path /private/worker")
	}}
	client := testClient(t, boundary.dependencies())
	base := "peen://session/" + uuid.NewString()
	for _, uri := range []string{
		base, base + "/messages", base + "/files/", base + "/files/a.txt",
	} {
		t.Run(uri, func(t *testing.T) {
			t.Parallel()
			failure := readFailure(t, client, uri)
			assert.EqualValues(t, jsonrpc.CodeInvalidParams, failure.Code)
			assert.Equal(t, failureDenied, failure.Message)
			assert.NotContains(t, string(failure.Data), "private")
			assert.Zero(t, boundary.accesses.Load())
		})
	}
}

func TestSystemSessionsHiddenFromResourcesAndCompletion(t *testing.T) {
	t.Parallel()
	system := uuid.New()
	boundary := &testBoundary{
		guard: func(context.Context, uuid.UUID) error {
			return commerr.ErrPermissionDenied
		},
		sessions: func(_ context.Context, p api.ListSessionsParams) (
			api.SessionPage, error,
		) {
			return api.SessionPage{
				Items: []api.Session{{Id: system}}, Limit: *p.Limit,
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	contents := readText(t, client, "peen://sessions")
	assert.NotContains(t, contents.Text, system.String())
	completed, err := client.Complete(t.Context(), &mcp.CompleteParams{
		Ref: &mcp.CompleteReference{
			Type: "ref/resource", URI: "peen://session/{sessionId}",
		},
		Argument: mcp.CompleteParamsArgument{Name: "sessionId"},
	})
	require.NoError(t, err)
	assert.Empty(t, completed.Completion.Values)
	paths, err := client.Complete(t.Context(), &mcp.CompleteParams{
		Ref: &mcp.CompleteReference{
			Type: "ref/resource",
			URI:  "peen://session/{sessionId}/files/{+path}",
		},
		Argument: mcp.CompleteParamsArgument{Name: "path"},
		Context: &mcp.CompleteContext{Arguments: map[string]string{
			"sessionId": system.String(),
		}},
	})
	require.NoError(t, err)
	assert.Empty(t, paths.Completion.Values)
}

func TestInternalErrorsStayPrivate(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{run: func(
		context.Context, control.ConversationRequest,
	) (*control.ConversationResult, error) {
		return nil, ctxerrors.New("private worker path and credential detail")
	}}
	client := testClient(t, boundary.dependencies())
	result := callTool(t, client, toolSendMessage,
		map[string]any{"workspace": "/workspace", "message": "hello"})
	require.True(t, result.IsError)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, failureInternal, text.Text)
}

func TestSchemaSizeBound(t *testing.T) {
	t.Parallel()
	deps := (&testBoundary{}).dependencies()
	deps.MaxOutputSchemaBytes = 2
	client := testClient(t, deps)
	result := callTool(t, client, toolSendMessage, map[string]any{
		"workspace": "/workspace", "message": "hello",
		"outputSchema": map[string]any{"type": "object"},
	})
	assert.True(t, result.IsError)
}
