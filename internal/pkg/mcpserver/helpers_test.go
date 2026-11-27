package mcpserver

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testBoundary struct {
	accesses atomic.Int32
	run      func(context.Context, control.ConversationRequest) (
		*control.ConversationResult, error,
	)
	guard    func(context.Context, uuid.UUID) error
	sessions func(context.Context, api.ListSessionsParams) (
		api.SessionPage, error,
	)
	messages func(context.Context, api.ListMessagesParams) (
		*api.MessagePage, error,
	)
	listing func(context.Context, uuid.UUID, string) (
		control.WorkspaceFileListing, error,
	)
	read func(context.Context, uuid.UUID, string) (
		control.WorkspaceFileContent, error,
	)
}

func (f *testBoundary) dependencies() Dependencies {
	return Dependencies{Runner: f, Sessions: f, Reader: f, Turns: f, Files: f}
}

func (f *testBoundary) Run(ctx context.Context, r control.ConversationRequest) (
	*control.ConversationResult, error,
) {
	f.accesses.Add(1)

	if f.run != nil {
		return f.run(ctx, r)
	}

	return &control.ConversationResult{
		SessionID: uuid.New(), Text: "reply",
	}, nil
}

func (f *testBoundary) ValidateWorkspaceSession(
	ctx context.Context, id uuid.UUID,
) error {
	if f.guard != nil {
		return f.guard(ctx, id)
	}

	return nil
}

func (f *testBoundary) ListSessions(
	ctx context.Context, p api.ListSessionsParams,
) (api.SessionPage, error) {
	f.accesses.Add(1)

	if f.sessions != nil {
		return f.sessions(ctx, p)
	}

	return api.SessionPage{Items: []api.Session{}, Limit: *p.Limit}, nil
}

func (f *testBoundary) Session(
	_ context.Context, id uuid.UUID,
) (*api.Session, error) {
	f.accesses.Add(1)

	return &api.Session{Id: id, Workspace: "/workspace"}, nil
}

func (f *testBoundary) ListMessages(
	ctx context.Context, p api.ListMessagesParams,
) (*api.MessagePage, error) {
	f.accesses.Add(1)

	if f.messages != nil {
		return f.messages(ctx, p)
	}

	return &api.MessagePage{Items: []api.Message{}, Limit: *p.Limit}, nil
}

func (f *testBoundary) ListModels() api.ModelList {
	return api.ModelList{Models: []api.Model{{Name: "example/model"}}}
}

func (f *testBoundary) CancelSessionTurn(
	_ context.Context, _ uuid.UUID,
) (bool, error) {
	f.accesses.Add(1)

	return true, nil
}

func testClient(t *testing.T, deps Dependencies) *mcp.ClientSession {
	t.Helper()

	return testClientWith(t, deps, nil)
}

func testClientWith(
	t *testing.T, deps Dependencies, options *mcp.ClientOptions,
) *mcp.ClientSession {
	t.Helper()
	server, err := New(deps)
	require.NoError(t, err)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{
		Name: "test", Version: "1",
	}, options)
	cs, err := client.Connect(t.Context(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cs.Close())
		require.NoError(t, ss.Close())
	})

	return cs
}

func callTool(
	t *testing.T, c *mcp.ClientSession, name string, args any,
) *mcp.CallToolResult {
	t.Helper()
	result, err := c.CallTool(t.Context(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	require.NoError(t, err)

	return result
}

// readFailure reads a resource that must fail and returns its JSON-RPC error.
func readFailure(
	t *testing.T, c *mcp.ClientSession, uri string,
) *jsonrpc.Error {
	t.Helper()
	result, err := c.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: uri,
	})
	require.Error(t, err, "%+v", result)
	var failure *jsonrpc.Error
	require.ErrorAs(t, err, &failure)

	return failure
}

// readText reads a resource with one text part and returns that part.
func readText(
	t *testing.T, c *mcp.ClientSession, uri string,
) *mcp.ResourceContents {
	t.Helper()
	result, err := c.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: uri,
	})
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, uri, result.Contents[0].URI)

	return result.Contents[0]
}

//nolint:ireturn // The type parameter is the decoded JSON value.
func decodeResult[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	require.False(t, result.IsError, "%+v", result.Content)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var value T
	require.NoError(t, json.Unmarshal(encoded, &value))

	return value
}
