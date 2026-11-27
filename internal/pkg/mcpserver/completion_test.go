package mcpserver

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func completeArgument(
	t *testing.T, c *mcp.ClientSession, params *mcp.CompleteParams,
) mcp.CompletionResultDetails {
	t.Helper()
	result, err := c.Complete(t.Context(), params)
	require.NoError(t, err)

	return result.Completion
}

func sessionCompletion(uri, value string) *mcp.CompleteParams {
	return &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: uri},
		Argument: mcp.CompleteParamsArgument{Name: "sessionId", Value: value},
	}
}

func TestSessionIDCompletion(t *testing.T) {
	t.Parallel()
	shown := uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001")
	other := uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000002")
	hidden := uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000003")
	boundary := &testBoundary{
		guard: func(_ context.Context, id uuid.UUID) error {
			if id == hidden {
				return commerr.ErrPermissionDenied
			}

			return nil
		},
		sessions: func(_ context.Context, p api.ListSessionsParams) (
			api.SessionPage, error,
		) {
			return api.SessionPage{
				Items: []api.Session{{Id: shown}, {Id: other}, {Id: hidden}},
				Limit: *p.Limit,
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	for _, uri := range []string{
		"peen://session/{sessionId}",
		"peen://session/{sessionId}/messages",
		"peen://session/{sessionId}/files/{+path}",
	} {
		values := completeArgument(t, client, sessionCompletion(uri, "AAAA"))
		assert.Equal(t, []string{shown.String()}, values.Values, uri)
		assert.False(t, values.HasMore)
	}
	all := completeArgument(t, client,
		sessionCompletion("peen://session/{sessionId}", ""))
	assert.ElementsMatch(t, []string{shown.String(), other.String()},
		all.Values)
	unknown := completeArgument(t, client, sessionCompletion("peen://elsewhere", ""))
	assert.Empty(t, unknown.Values)
}

func TestSessionIDCompletionIsBounded(t *testing.T) {
	t.Parallel()
	pages := make(chan int32, 10)
	boundary := &testBoundary{sessions: func(
		_ context.Context, p api.ListSessionsParams,
	) (api.SessionPage, error) {
		offset := *p.Offset
		pages <- offset
		items := make([]api.Session, 0, 15)
		for range 15 {
			items = append(items, api.Session{Id: uuid.New()})
		}

		return api.SessionPage{
			Items: items, Limit: *p.Limit, Offset: offset, HasMore: true,
		}, nil
	}}
	client := testClient(t, boundary.dependencies())
	values := completeArgument(t, client,
		sessionCompletion("peen://session/{sessionId}", ""))
	assert.Len(t, values.Values, 20)
	assert.True(t, values.HasMore)
	assert.Equal(t, int32(0), <-pages)
	assert.Equal(t, int32(200), <-pages)
	assert.Empty(t, pages)
}

func TestPathCompletion(t *testing.T) {
	t.Parallel()
	listed := make(chan string, 4)
	boundary := &testBoundary{listing: func(
		_ context.Context, _ uuid.UUID, path string,
	) (control.WorkspaceFileListing, error) {
		listed <- path
		entries := []control.WorkspaceFileEntry{
			{Name: "main.go"}, {Name: "manual.md"}, {Name: "notes.txt"},
		}

		return control.WorkspaceFileListing{
			Path:    path,
			Type:    api.WorkspaceFileTypeDirectory,
			Entries: entries,
		}, nil
	}}
	client := testClient(t, boundary.dependencies())
	id := uuid.NewString()
	for _, tc := range []struct {
		value  string
		parent string
		values []string
	}{
		{"ma", "", []string{"main.go", "manual.md"}},
		{"src/n", "src", []string{"src/notes.txt"}},
		{"src/pkg/", "src/pkg", []string{
			"src/pkg/main.go", "src/pkg/manual.md", "src/pkg/notes.txt",
		}},
	} {
		values := completeArgument(t, client, &mcp.CompleteParams{
			Ref: &mcp.CompleteReference{
				Type: "ref/resource",
				URI:  "peen://session/{sessionId}/files/{+path}",
			},
			Argument: mcp.CompleteParamsArgument{
				Name: "path", Value: tc.value,
			},
			Context: &mcp.CompleteContext{Arguments: map[string]string{
				"sessionId": id,
			}},
		})
		assert.Equal(t, tc.values, values.Values, tc.value)
		assert.Equal(t, tc.parent, <-listed)
	}
}

func TestPathCompletionRefusals(t *testing.T) {
	t.Parallel()
	boundary := &testBoundary{}
	client := testClient(t, boundary.dependencies())
	for index, tc := range []struct {
		uri     string
		session string
		value   string
	}{
		{"peen://session/{sessionId}/files/{+path}", "", "a"},
		{"peen://session/{sessionId}/files/{+path}", "bad", "a"},
		{
			"peen://session/{sessionId}/files/{+path}", uuid.NewString(),
			"../a",
		},
		{
			"peen://session/{sessionId}/files/{+path}", uuid.NewString(),
			"/etc/p",
		},
		{"peen://session/{sessionId}", uuid.NewString(), "a"},
	} {
		values := completeArgument(t, client, &mcp.CompleteParams{
			Ref: &mcp.CompleteReference{Type: "ref/resource", URI: tc.uri},
			Argument: mcp.CompleteParamsArgument{
				Name: "path", Value: tc.value,
			},
			Context: &mcp.CompleteContext{Arguments: map[string]string{
				"sessionId": tc.session,
			}},
		})
		assert.Empty(t, values.Values, fmt.Sprint(index))
	}
	assert.Zero(t, boundary.accesses.Load())
}
