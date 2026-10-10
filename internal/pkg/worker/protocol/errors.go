package protocol

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ErrWorkerRejected reports a registration the controller refused. A worker
// that sees it must exit rather than retry: the credential, session, or
// generation it was launched with is not one the controller will accept.
var ErrWorkerRejected = errors.New("worker registration rejected")

// Code is the transport-neutral error class carried on a result frame.
//
// It exists so a failure keeps its meaning across the socket. Without it a
// worker would see every controller refusal as one opaque string and could not
// tell a busy session from a missing record.
type Code string

const (
	CodeUnknown          Code = "unknown"
	CodeNotFound         Code = "not_found"
	CodeAlreadyExists    Code = "already_exists"
	CodeConflict         Code = "conflict"
	CodeValidation       Code = "validation_failed"
	CodePermissionDenied Code = "permission_denied"
	CodeLockHeld         Code = "lock_held"
	CodeCancelled        Code = "cancelled"
	CodeNotImplemented   Code = "not_implemented"

	CodeRunningTurnSettingsChange Code = "running_turn_settings_change"
	CodeUserMessageQueueFull      Code = "user_message_queue_full"
)

// Error is one failure as it crosses the socket.
type Error struct {
	Code Code `json:"code"`
	// Message is operator-facing text. It never carries a credential, because
	// the controller only ever puts its own wrap messages here.
	Message string `json:"message"`
}

// sentinels maps each transport code to the shared sentinel it stands for.
//
//nolint:gochecknoglobals // A lookup table both directions read.
var sentinels = map[Code]error{
	CodeNotFound:         commerr.ErrNotFound,
	CodeAlreadyExists:    commerr.ErrAlreadyExists,
	CodeConflict:         commerr.ErrConflict,
	CodeValidation:       commerr.ErrValidationFailed,
	CodePermissionDenied: commerr.ErrPermissionDenied,
	CodeLockHeld:         commerr.ErrLockHeld,
	CodeCancelled:        commerr.ErrCancelled,
	CodeNotImplemented:   commerr.ErrNotImplemented,
}

// detailedFailure is a failure more specific than its shared class. The
// controller tells a person exactly why their message was refused, so the
// detail has to survive the socket rather than collapse into the class.
type detailedFailure struct {
	code   Code
	detail error
	class  error
}

// detailedFailures is checked before sentinels, because an error carrying a
// detail also matches its class.
//
//nolint:gochecknoglobals // A lookup table both directions read.
var detailedFailures = []detailedFailure{
	{
		code:   CodeRunningTurnSettingsChange,
		detail: session.ErrRunningTurnSettingsChange,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeUserMessageQueueFull,
		detail: elelem.ErrUserMessageQueueFull,
		class:  commerr.ErrConflict,
	},
}

// codeFor classifies an error for the wire. A detailed failure wins over its
// class. Otherwise each check is exclusive in practice; an unrecognised
// failure stays unknown rather than being forced into a class it does not
// belong to.
func codeFor(err error) Code {
	for _, failure := range detailedFailures {
		if errors.Is(err, failure.detail) {
			return failure.code
		}
	}

	for code, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			return code
		}
	}

	return CodeUnknown
}

// NewError converts a controller-side failure into a wire error.
func NewError(err error) *Error {
	if err == nil {
		return nil
	}

	return &Error{Code: codeFor(err), Message: err.Error()}
}

// Unwrap rebuilds a Go error from a wire error, restoring the sentinel so
// errors.Is still answers correctly on the receiving side. A detailed failure
// comes back matching both its detail and its class.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	for _, failure := range detailedFailures {
		if failure.code == e.Code {
			return ctxerrors.Wrap(
				errors.Join(failure.class, failure.detail),
				e.Message,
			)
		}
	}

	sentinel, found := sentinels[e.Code]
	if !found {
		return ctxerrors.New(e.Message)
	}

	return ctxerrors.Wrap(sentinel, e.Message)
}
