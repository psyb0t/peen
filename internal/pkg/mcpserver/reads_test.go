package mcpserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPagesPreserveRawOffsets(t *testing.T) {
	t.Parallel()
	hidden, visible := uuid.New(), uuid.New()
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
			offset := int32(0)
			if p.Offset != nil {
				offset = *p.Offset
			}
			id := hidden
			if offset != 0 {
				id = visible
			}

			return api.SessionPage{
				Items: []api.Session{{Id: id}},
				Limit: *p.Limit, Offset: offset, HasMore: offset == 0,
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	first := decodeResult[api.SessionPage](t,
		callTool(t, client, toolListSessions, map[string]any{"limit": 1}))
	assert.Empty(t, first.Items)
	require.True(t, first.HasMore)
	second := decodeResult[api.SessionPage](t,
		callTool(t, client, toolListSessions, map[string]any{
			"limit": first.Limit, "offset": first.Offset + first.Limit,
		}))
	require.Len(t, second.Items, 1)
	assert.Equal(t, visible, second.Items[0].Id)
	assert.False(t, second.HasMore)
}

func TestReadTools(t *testing.T) {
	t.Parallel()
	params := make(chan api.ListMessagesParams, 1)
	boundary := &testBoundary{messages: func(
		_ context.Context, p api.ListMessagesParams,
	) (*api.MessagePage, error) {
		params <- p

		return &api.MessagePage{Items: []api.Message{
			{Content: "newest"}, {Content: "older"},
		}, Limit: *p.Limit}, nil
	}}
	client := testClient(t, boundary.dependencies())
	id := uuid.New()
	args := map[string]any{"sessionId": id.String()}
	stored := decodeResult[api.Session](t,
		callTool(t, client, toolGetSession, args))
	assert.Equal(t, id, stored.Id)
	page := decodeResult[api.MessagePage](t,
		callTool(t, client, toolReadMessages, args))
	require.Len(t, page.Items, 2)
	assert.Equal(t, "older", page.Items[0].Content)
	assert.Equal(t, "newest", page.Items[1].Content)
	request := <-params
	assert.Equal(t, api.ListMessagesParamsOrderDesc, *request.Order)
	assert.EqualValues(t, 50, *request.Limit)
	models := decodeResult[api.ModelList](t,
		callTool(t, client, toolListModels, map[string]any{}))
	require.Len(t, models.Models, 1)
	assert.Equal(t, "example/model", models.Models[0].Name)
	cancelled := decodeResult[cancelResult](t,
		callTool(t, client, toolCancelTurn, args))
	assert.True(t, cancelled.Cancelled)
}

func TestPaginationBounds(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	for _, limit := range []int{-1, 0, 201} {
		assert.True(t, callTool(t, client, toolListSessions,
			map[string]any{"limit": limit}).IsError)
		assert.True(t, callTool(t, client, toolReadMessages,
			map[string]any{
				"sessionId": uuid.NewString(), "limit": limit,
			}).IsError)
	}
	assert.True(t, callTool(t, client, toolListSessions,
		map[string]any{"offset": -1}).IsError)
}
