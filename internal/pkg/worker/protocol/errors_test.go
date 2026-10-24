package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorPreservesInvalidStateAcrossTheWire(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(NewError(ctxerrors.Wrap(
		commerr.ErrInvalidState, "inactive turn lease")))
	require.NoError(t, err)
	var decoded Error
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, CodeInvalidState, decoded.Code)
	require.ErrorIs(t, decoded.Unwrap(), commerr.ErrInvalidState)
}

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

// A message that names an unknown skill is refused as invalid input, and the
// controller side still sees it as a validation failure.
func TestErrorKeepsAnUnknownSkillReferenceAsAValidationFailure(t *testing.T) {
	t.Parallel()

	const wantMessage = `unknown skill "x" in :x; available skills: a, core:planning`

	sent := ctxerrors.Wrap(
		errors.Join(
			commerr.ErrValidationFailed,
			harness.NewUnknownSkillError("x", []string{"a", "core:planning"}),
		),
		"resolve skill",
	)

	encoded, err := json.Marshal(NewError(sent))
	require.NoError(t, err)

	decoded := Error{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, CodeUnknownSkill, decoded.Code)
	assert.Equal(t, wantMessage, decoded.Message)

	received := decoded.Unwrap()
	require.ErrorIs(t, received, commerr.ErrValidationFailed)
	require.ErrorIs(t, received, harness.ErrSkillNotFound)

	var unknown *harness.UnknownSkillError
	require.ErrorAs(t, received, &unknown)
	assert.Equal(t, wantMessage, unknown.Message)
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

// A message to an ended or unknown child keeps its reason across the wire, so
// the WebSocket can tell the sender which one it was.
func TestErrorKeepsAgentRunRefusalsAcrossTheWire(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		detail   error
		class    error
		wantCode Code
	}{
		{
			name:     "finished",
			detail:   session.ErrAgentRunFinished,
			class:    commerr.ErrConflict,
			wantCode: CodeAgentRunFinished,
		},
		{
			name:     "not found",
			detail:   session.ErrAgentRunNotFound,
			class:    commerr.ErrNotFound,
			wantCode: CodeAgentRunNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sent := ctxerrors.Wrap(
				errors.Join(tc.class, tc.detail),
				"message the agent run",
			)

			wire := NewError(sent)
			assert.Equal(t, tc.wantCode, wire.Code)

			received := wire.Unwrap()
			require.ErrorIs(t, received, tc.detail)
			require.ErrorIs(t, received, tc.class)
		})
	}
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

func TestErrorKeepsPendingInboxIdentityAcrossJSON(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(NewError(ctxerrors.Wrap(
		errors.Join(commerr.ErrConflict, session.ErrExecutionInboxPending),
		"settle execution inbox")))
	require.NoError(t, err)
	var decoded Error
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, CodeExecutionInboxPending, decoded.Code)
	require.ErrorIs(t, decoded.Unwrap(), session.ErrExecutionInboxPending)
	require.ErrorIs(t, decoded.Unwrap(), commerr.ErrConflict)
	plain := NewError(ctxerrors.Wrap(commerr.ErrConflict, "stale claim"))
	assert.Equal(t, CodeConflict, plain.Code)
	assert.NotErrorIs(t, plain.Unwrap(), session.ErrExecutionInboxPending)
}
