package protocol

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalTurnCancellationCannotReachAReplacement(t *testing.T) {
	t.Parallel()
	sessionID, turnID := uuid.New(), uuid.New()
	store, peer := newStoreHarness(t, func(Frame) Frame {
		return resultWith(t, session.Lease{
			ID: uuid.New(), SessionID: sessionID, TurnID: turnID,
		})
	})
	lease, err := store.AcquireTurn(t.Context(), sessionID,
		session.StartTurnInput{})
	require.NoError(t, err)
	peer.awaitCall(t)
	var cancelled atomic.Bool
	store.RegisterCancellation(lease, func() { cancelled.Store(true) })
	assert.False(t, store.RequestLocalTurnCancellation(sessionID, uuid.New()))
	assert.False(t, store.RequestLocalTurnCancellation(uuid.New(), turnID))
	assert.False(t, store.RequestLocalTurnCancellation(sessionID, uuid.Nil))
	assert.False(t, cancelled.Load())
	assert.True(t, store.RequestLocalTurnCancellation(sessionID, turnID))
	assert.True(t, cancelled.Load())
}

func TestWorkerExactCancellationCarriesItsTurnID(t *testing.T) {
	t.Parallel()
	store, peer := newStoreHarness(t, func(Frame) Frame {
		return resultWith(t, CancelResult{CancelRequested: true})
	})
	worker := &Worker{conn: store.conn}
	_, err := worker.CancelTurn(t.Context(), uuid.Nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	turnID := uuid.New()
	requested, err := worker.CancelTurn(t.Context(), turnID)
	require.NoError(t, err)
	assert.True(t, requested)
	frame := peer.awaitCall(t)
	assert.Equal(t, string(CommandCancelTurn), frame.Method)
	var payload CancelTurn
	require.NoError(t, json.Unmarshal(frame.Payload, &payload))
	assert.Equal(t, turnID, payload.TurnID)
}

func TestWorkerExactCancellationNeverFallsBackToASessionStop(t *testing.T) {
	t.Parallel()
	store, peer := newStoreHarness(t, func(Frame) Frame {
		return Frame{
			Kind:  KindCommandResult,
			Error: NewError(commerr.ErrNotImplemented),
		}
	})
	worker := &Worker{conn: store.conn}
	_, err := worker.CancelTurn(t.Context(), uuid.New())
	require.Error(t, err)
	assert.Equal(t, string(CommandCancelTurn), peer.awaitCall(t).Method)
	select {
	case frame := <-peer.calls:
		t.Fatalf("unexpected fallback command: %s", frame.Method)
	default:
	}
}
