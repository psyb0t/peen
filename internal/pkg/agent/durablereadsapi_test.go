package agent

import (
	"context"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One completed turn is enough to populate every durable read this file serves:
// the event log, the turn lifecycle, and the two snapshots the turn pinned.
func TestRuntimeServesTheDurableReadsOfACompletedTurn(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

	turnResult, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "do the work",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	t.Run("events", func(t *testing.T) {
		page, err := fixture.runtime.ListSessionEvents(
			ctx,
			api.ListSessionEventsParams{XSessionID: turnResult.SessionID},
		)
		require.NoError(t, err)
		require.NotEmpty(
			t,
			page.Events,
			"a completed turn records its event log durably",
		)

		for _, event := range page.Events {
			assert.NotEmpty(t, event.Type)
		}
	})

	t.Run("events honour order and paging", func(t *testing.T) {
		limit := int32(1)
		ascending := api.ListSessionEventsParamsOrderAsc
		descending := api.ListSessionEventsParamsOrderDesc

		first, err := fixture.runtime.ListSessionEvents(
			ctx,
			api.ListSessionEventsParams{
				XSessionID: turnResult.SessionID,
				Limit:      &limit,
				Order:      &ascending,
			},
		)
		require.NoError(t, err)
		require.Len(t, first.Events, 1)
		assert.Equal(t, limit, first.Limit)

		last, err := fixture.runtime.ListSessionEvents(
			ctx,
			api.ListSessionEventsParams{
				XSessionID: turnResult.SessionID,
				Limit:      &limit,
				Order:      &descending,
			},
		)
		require.NoError(t, err)
		require.Len(t, last.Events, 1)

		// Ascending and descending must not return the same first row, or the
		// order parameter is being ignored.
		assert.NotEqual(t, first.Events[0].Sequence, last.Events[0].Sequence)
	})

	t.Run("turns", func(t *testing.T) {
		page, err := fixture.runtime.ListSessionTurns(
			ctx,
			api.ListSessionTurnsParams{XSessionID: turnResult.SessionID},
		)
		require.NoError(t, err)
		require.Len(t, page.Turns, 1)

		turn := page.Turns[0]
		assert.Equal(t, turnResult.SessionID, turn.SessionId)
		assert.NotEmpty(t, turn.State)
	})

	t.Run("snapshots", func(t *testing.T) {
		query := repositories.Use(fixture.handle.GormDB)
		stored, err := query.Turn.WithContext(ctx).
			Where(query.Turn.SessionID.Eq(turnResult.SessionID)).
			First()
		require.NoError(t, err)

		require.NotNil(
			t,
			stored.ContextSnapshotHash,
			"a completed turn pins the context it ran against",
		)
		contextSnapshot, err := fixture.runtime.GetSessionContextSnapshot(
			ctx,
			turnResult.SessionID,
			*stored.ContextSnapshotHash,
		)
		require.NoError(t, err)
		assert.Equal(t, *stored.ContextSnapshotHash, contextSnapshot.Hash)
		assert.NotEmpty(
			t,
			contextSnapshot.Manifest,
			"the manifest decodes as the array the harness wrote",
		)

		require.NotNil(t, stored.PromptSnapshotHash)
		prompt, err := fixture.runtime.GetSessionPromptSnapshot(
			ctx,
			turnResult.SessionID,
			*stored.PromptSnapshotHash,
		)
		require.NoError(t, err)
		assert.Equal(t, *stored.PromptSnapshotHash, prompt.Hash)
		assert.NotEmpty(t, prompt.EffectivePrompt)
	})
}

// An unknown hash is a not-found read rather than an empty snapshot, so a
// client cannot mistake a typo for a turn that recorded nothing.
func TestRuntimeFailsToReadAnUnknownSnapshot(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

	turnResult, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "do the work",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	snapshot, err := fixture.runtime.GetSessionContextSnapshot(
		ctx,
		turnResult.SessionID,
		"not-a-hash",
	)
	require.Error(t, err)
	assert.Nil(t, snapshot)

	prompt, err := fixture.runtime.GetSessionPromptSnapshot(
		ctx,
		turnResult.SessionID,
		"not-a-hash",
	)
	require.Error(t, err)
	assert.Nil(t, prompt)
}

// A malformed page request is rejected before it reaches SQLite.
func TestRuntimeRejectsMalformedDurableReadPaging(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

	turnResult, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "do the work",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	negative := int32(-1)

	events, err := fixture.runtime.ListSessionEvents(
		ctx,
		api.ListSessionEventsParams{
			XSessionID: turnResult.SessionID,
			Limit:      &negative,
		},
	)
	require.Error(t, err)
	assert.Nil(t, events)

	turns, err := fixture.runtime.ListSessionTurns(
		ctx,
		api.ListSessionTurnsParams{
			XSessionID: turnResult.SessionID,
			Offset:     &negative,
		},
	)
	require.Error(t, err)
	assert.Nil(t, turns)
}
