package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type childPhaseWindowStore struct {
	*session.Store
	readErr error
	pageErr error
	empty   bool
	gap     bool
}

func (s *childPhaseWindowStore) GetAgentRun(
	ctx context.Context,
	sessionID, runID uuid.UUID,
) (*models.AgentRun, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}

	return s.Store.GetAgentRun(ctx, sessionID, runID)
}

func (s *childPhaseWindowStore) ListAgentRunEvents(
	ctx context.Context,
	sessionID, runID uuid.UUID,
	options session.ListAgentRunEventsOptions,
) (*session.AgentRunEventPage, error) {
	if s.pageErr != nil {
		return nil, s.pageErr
	}
	if s.empty {
		return &session.AgentRunEventPage{}, nil
	}

	page, err := s.Store.ListAgentRunEvents(ctx, sessionID, runID, options)
	if err != nil {
		return nil, err
	}
	if s.gap && len(page.Items) > 0 {
		page.Items[0].Sequence = 0
	}

	return page, nil
}

func TestChildPhaseEventsPreserveWindowOnReadFailure(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		store   childPhaseWindowStore
		wantErr error
	}{
		{
			name: "run read", wantErr: commerr.ErrConflict,
			store: childPhaseWindowStore{readErr: commerr.ErrConflict},
		},
		{
			name: "page read", wantErr: commerr.ErrConflict,
			store: childPhaseWindowStore{pageErr: commerr.ErrConflict},
		},
		{
			name: "missing", wantErr: commerr.ErrNotFound,
			store: childPhaseWindowStore{empty: true},
		},
		{
			name: "gap", wantErr: commerr.ErrConflict,
			store: childPhaseWindowStore{gap: true},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, dispatch, input, _ := childPhaseProjectionFixture(t, nil)
			shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
			require.NoError(t, err)
			before, next, dropped := shared.actor.run.ReadEvents(0, 0)
			tc.store.Store = fixture.base.store
			fixture.base.runtime.store = &tc.store
			err = dispatch.child.refreshChildPhaseEvents(t.Context(), shared.actor)
			require.ErrorIs(t, err, tc.wantErr)
			after, afterNext, afterDropped := shared.actor.run.ReadEvents(0, 0)
			assert.Equal(t, before, after)
			assert.Equal(t, next, afterNext)
			assert.Equal(t, dropped, afterDropped)
		})
	}
}

func TestChildPhaseEventsReplaceBoundedWindow(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		maxCount int
		maxBytes int
		count    int
	}{
		{name: "count", maxCount: 2, maxBytes: 1000, count: 2},
		{name: "bytes", maxCount: 10, maxBytes: 1, count: 1},
		{name: "all", maxCount: 10, maxBytes: 1000, count: 3},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buffer := newAgentRunEventBuffer(tc.maxCount, tc.maxBytes)
			retained := newAgentRunEventBuffer(tc.maxCount, tc.maxBytes)
			for sequence := range 3 {
				retained.AppendStored(AgentRunEvent{
					Sequence: sequence + 1, Type: EventTypeAgentRunToolUse,
					Payload: json.RawMessage(`{}`),
				})
			}
			buffer.replaceStoredWindow(retained)
			got, next, dropped := buffer.Read(0, 0)
			require.Len(t, got, tc.count)
			assert.Equal(t, 3, next)
			assert.Equal(t, 3-tc.count, dropped)
			buffer.replaceStoredWindow(newAgentRunEventBuffer(10, 1000))
			after, afterNext, afterDropped := buffer.Read(0, 0)
			assert.Equal(t, got, after)
			assert.Equal(t, next, afterNext)
			assert.Equal(t, dropped, afterDropped)
		})
	}
}

func TestChildPhaseEventsRefreshCountBound(t *testing.T) {
	t.Parallel()
	fixture, dispatch, input, runID := childPhaseProjectionFixture(t, nil)
	shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
	require.NoError(t, err)
	shared.actor.run.events.maxCount = 2
	for range 3 {
		_, err := fixture.base.store.AppendAgentRunEvent(t.Context(),
			input.Tool.Round.Execution.SessionID, runID,
			session.AgentRunEventInput{
				ID: uuid.New(), EventType: EventTypeAgentRunToolUse,
				PayloadJSON: `{}`,
			})
		require.NoError(t, err)
	}
	require.NoError(t, dispatch.child.refreshChildPhaseEvents(t.Context(),
		shared.actor))
	got, next, dropped := shared.actor.run.ReadEvents(0, 0)
	require.Len(t, got, 2)
	assert.Equal(t, 4, next)
	assert.Equal(t, 2, dropped)
	assert.Equal(t, 3, got[0].Sequence)
	assert.Equal(t, 4, got[1].Sequence)
}
