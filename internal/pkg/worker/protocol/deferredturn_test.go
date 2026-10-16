package protocol

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeferredTurnRPCRetainsOpeningBatch(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, hook := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.ReserveExecutionCompletion(t.Context(), fixture.sessionID,
		hook.Lease)
	require.NoError(t, err)
	accepted, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
		lease, session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "retained request", Capacity: 1,
		})
	require.NoError(t, err)
	finishDeferredRPCSource(t, fixture, remote, execution.ID, lease, hook)
	create := func() *models.SessionNotice {
		t.Helper()
		notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
			session.CreateSessionNoticeInput{
				Type: "app.ready", Source: "example", Summary: "ready",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)

		return notice
	}
	input := session.StartTurnInput{
		RequestID: accepted.Message.RequestID,
		Workspace: accepted.Message.WorkingDirectory,
	}
	for range session.DefaultPageLimit + 3 {
		input.NoticeIDs = append(input.NoticeIDs, create().ID)
	}
	first, err := remote.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID, input)
	require.NoError(t, err)
	t.Cleanup(func() { remote.ReleaseTurn(first.Lease) })
	require.Len(t, first.Notices, len(input.NoticeIDs))
	for index, notice := range first.Notices {
		assert.Equal(t, input.NoticeIDs[index], notice.ID)
		assert.Equal(t, models.NoticeStateDelivered, notice.State)
	}
	newer := create()
	input.NoticeIDs = []uuid.UUID{newer.ID}
	repeated, err := remote.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first.Notices, repeated.Notices)
	pending, err := remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, newer.ID, pending.Items[0].ID)
}

func TestDeferredTurnRPCIsIdempotentAndFencesSession(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, hook := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.ReserveExecutionCompletion(t.Context(), fixture.sessionID,
		hook.Lease)
	require.NoError(t, err)
	accepted, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
		lease, session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "retained request", Capacity: 1,
		})
	require.NoError(t, err)
	finishDeferredRPCSource(t, fixture, remote, execution.ID, lease, hook)
	input := session.StartTurnInput{
		RequestID: accepted.Message.RequestID,
		Workspace: accepted.Message.WorkingDirectory,
		Events: []session.EventInput{{
			EventType: session.EventTypeUserMessageDelivered, PayloadJSON: `{}`,
		}},
	}
	first, err := remote.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID, input)
	require.NoError(t, err)
	t.Cleanup(func() { remote.ReleaseTurn(first.Lease) })
	repeated, err := remote.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first.Lease, repeated.Lease)
	assert.Empty(t, repeated.Events)
	_, err = remote.AcquireDeferredExecutionTurn(t.Context(), uuid.New(),
		accepted.Message.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}
