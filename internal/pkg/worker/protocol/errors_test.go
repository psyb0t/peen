package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal the controller shows a person must still be recognisable after it
// crosses the socket, and must still match its broader class for callers that
// only know the class.
func TestErrorKeepsADetailedFailureAcrossTheWire(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		detail   error
		wantCode Code
	}{
		{
			name:     "running turn settings change",
			detail:   session.ErrRunningTurnSettingsChange,
			wantCode: CodeRunningTurnSettingsChange,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sent := ctxerrors.Wrap(
				errors.Join(commerr.ErrValidationFailed, tc.detail),
				"queue active user message",
			)

			encoded, err := json.Marshal(NewError(sent))
			require.NoError(t, err)

			decoded := Error{}
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, tc.wantCode, decoded.Code)

			received := decoded.Unwrap()
			require.ErrorIs(t, received, tc.detail)
			require.ErrorIs(t, received, commerr.ErrValidationFailed)
		})
	}
}

// A full queue is refused differently from a busy session, so the WebSocket
// can tell the sender to wait rather than report a conflict.
func TestErrorKeepsAFullUserMessageQueueAcrossTheWire(t *testing.T) {
	t.Parallel()

	sent := ctxerrors.Wrap(
		errors.Join(commerr.ErrConflict, elelem.ErrUserMessageQueueFull),
		"queue active user message",
	)

	encoded, err := json.Marshal(NewError(sent))
	require.NoError(t, err)

	decoded := Error{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, CodeUserMessageQueueFull, decoded.Code)

	received := decoded.Unwrap()
	require.ErrorIs(t, received, elelem.ErrUserMessageQueueFull)
	require.ErrorIs(t, received, commerr.ErrConflict)
}

// A plain class still travels as that class and gains no detail.
func TestErrorKeepsAPlainClassAcrossTheWire(t *testing.T) {
	t.Parallel()

	sent := ctxerrors.Wrap(commerr.ErrValidationFailed, "message")

	wire := NewError(sent)
	assert.Equal(t, CodeValidation, wire.Code)

	received := wire.Unwrap()
	require.ErrorIs(t, received, commerr.ErrValidationFailed)
	assert.NotErrorIs(t, received, session.ErrRunningTurnSettingsChange)
}
