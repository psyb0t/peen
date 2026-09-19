package protocol

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
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

// codeFor classifies an error for the wire. The order matters only in that
// each check is exclusive in practice; an unrecognised failure stays unknown
// rather than being forced into a class it does not belong to.
func codeFor(err error) Code {
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
// errors.Is still answers correctly on the worker side.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	sentinel, found := sentinels[e.Code]
	if !found {
		return ctxerrors.New(e.Message)
	}

	return ctxerrors.Wrap(sentinel, e.Message)
}
