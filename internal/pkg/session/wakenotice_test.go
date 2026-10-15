package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeNoticeValidationIsReadOnlyAndRechecksAncestry(t *testing.T) {
	t.Parallel()
	fixture, child, notice := agentWakeFixture(t)
	options := ListSessionNoticesOptions{ID: &notice.ID, ValidateWakeSource: true}
	page, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID, child.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	options.ValidateWakeSource = false
	page, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.NoError(t, err)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
}

func TestWakeNoticeValidationRequiresExactPendingIdentity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		id   *uuid.UUID
	}{
		{name: "missing identity"}, {name: "zero identity", id: &uuid.Nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, _ := agentWakeFixture(t)
			_, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID, ListSessionNoticesOptions{ID: tc.id, ValidateWakeSource: true})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
	t.Run("already delivered", func(t *testing.T) {
		t.Parallel()
		fixture, _, notice := agentWakeFixture(t)
		_, err := fixture.store.DrainSessionNotices(t.Context(), fixture.sessionID)
		require.NoError(t, err)
		_, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID, ListSessionNoticesOptions{ID: &notice.ID, ValidateWakeSource: true})
		require.ErrorIs(t, err, commerr.ErrConflict)
	})
}
