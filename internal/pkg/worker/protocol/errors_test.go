package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
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
		{
			name:     "system prompt over budget",
			detail:   harness.ErrPromptBudgetExceeded,
			class:    commerr.ErrValidationFailed,
			wantCode: CodePromptBudgetExceeded,
		},
		{
			name:     "model takes no images",
			detail:   session.ErrImagesNotAccepted,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeImagesNotAccepted,
		},
		{
			name:     "history images on a text-only model",
			detail:   session.ErrHistoryImagesNotAccepted,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeHistoryImagesNotAccepted,
		},
		{
			name:     "images on a queued message",
			detail:   session.ErrImagesNeedNewTurn,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeImagesNeedNewTurn,
		},
		{
			name:     "too many images",
			detail:   session.ErrTooManyImages,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeTooManyImages,
		},
		{
			name:     "image too large",
			detail:   session.ErrImageTooLarge,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeImageTooLarge,
		},
		{
			name:     "image not its declared type",
			detail:   session.ErrImageType,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeImageType,
		},
		{
			name:     "message too large",
			detail:   session.ErrMessageTooLarge,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeMessageTooLarge,
		},
		{
			name:     "system prompt too large",
			detail:   session.ErrSystemPromptTooLarge,
			class:    commerr.ErrValidationFailed,
			wantCode: CodeSystemPromptTooLarge,
		},
		{
			name:     "turn already ended",
			detail:   session.ErrTurnLeaseInactive,
			class:    commerr.ErrInvalidState,
			wantCode: CodeTurnLeaseInactive,
		},
		{
			name:     "turn round limit",
			detail:   session.ErrTurnRoundLimit,
			class:    commerr.ErrOutOfRange,
			wantCode: CodeTurnRoundLimit,
		},
		{
			name:     "turn token limit",
			detail:   session.ErrTurnTokenLimit,
			class:    commerr.ErrOutOfRange,
			wantCode: CodeTurnTokenLimit,
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

// The worker raises a harness refusal bare, without a class. It still has to
// reach the controller as that refusal, which reports it as
// HARNESS_CONFIGURATION_INVALID instead of an internal failure.
func TestErrorCarriesAHarnessRefusalAcrossTheWire(t *testing.T) {
	t.Parallel()

	refusals := []error{
		harness.ErrInvalidInstruction,
		harness.ErrInvalidSkill,
		harness.ErrInvalidAgent,
		harness.ErrInvalidEventHandler,
		harness.ErrInvalidHook,
		harness.ErrResourceLimit,
		harness.ErrUnreadableLayer,
	}

	for _, refusal := range refusals {
		t.Run(refusal.Error(), func(t *testing.T) {
			t.Parallel()

			sent := ctxerrors.Wrap(
				ctxerrors.Wrap(refusal, "instruction limit exceeded"),
				"resolve harness context",
			)

			wire := NewError(sent)
			assert.NotEqual(t, CodeUnknown, wire.Code)
			require.ErrorIs(t, wire.Unwrap(), refusal)
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

// A turn limit reaches the controller with its own text, which names the
// setting that raised it, and not with the chain of wraps around it.
func TestErrorCarriesATurnLimitTextAcrossTheWire(t *testing.T) {
	t.Parallel()

	const limitText = "The turn stopped at its limit of 2 tool rounds (PEEN_MAX_TOOL_ROUNDS)."

	sent := ctxerrors.Wrap(
		publicerror.WithMessage(limitText, errors.Join(
			session.ErrTurnRoundLimit, commerr.ErrOutOfRange)),
		"wait for native root result",
	)

	encoded, err := json.Marshal(NewError(sent))
	require.NoError(t, err)

	var decoded Error
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, CodeTurnRoundLimit, decoded.Code)
	assert.Equal(t, limitText, decoded.Message)

	received := decoded.Unwrap()
	require.ErrorIs(t, received, session.ErrTurnRoundLimit)
	require.ErrorIs(t, received, commerr.ErrOutOfRange)
	assert.Equal(t, limitText, publicerror.Message(received))
}
