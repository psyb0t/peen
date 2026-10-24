package workflowerrors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"

	"github.com/psyb0t/go-workflows/internal/sync"
)

const canceledCode = "canceled"

type Error struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`

	Permanent  bool     `json:"permanent,omitempty"`
	Cause      error    `json:"cause,omitempty"`
	Stacktrace string   `json:"stacktrace,omitempty"`
	Code       string   `json:"code,omitempty"`
	Causes     []*Error `json:"causes,omitempty"`
}

func (we *Error) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return ctxerrors.Wrap(commerr.ErrInvalidValue, "workflow error must be an object")
	}
	type Alias Error
	var decoded Alias
	a := &struct {
		Cause *Error `json:"cause,omitempty"`
		*Alias
	}{Alias: &decoded}

	if err := json.Unmarshal(b, a); err != nil {
		return ctxerrors.Wrap(err, "decode workflow error")
	}

	*we = *(*Error)(a.Alias)
	we.Cause = a.Cause

	return nil
}

func (we *Error) Error() string {
	return we.Message
}

func (we *Error) Unwrap() error {
	if we == nil || we.Cause == (*Error)(nil) {
		return nil
	}

	return we.Cause
}

func (we *Error) Stack() string {
	return we.Stacktrace
}

func (we *Error) Is(target error) bool {
	if we == nil {
		return false
	}
	if we.Code == canceledCode && (target == sync.Canceled || target == context.Canceled) {
		return true
	}
	for _, cause := range we.Causes {
		if errors.Is(cause, target) {
			return true
		}
	}
	return false
}

func (we *Error) As(target any) bool {
	if we == nil {
		return false
	}
	for _, cause := range we.Causes {
		if errors.As(ToError(cause), target) {
			return true
		}
	}
	return false
}

var _ error = (*Error)(nil)

// FromError wraps the given error into a workflow error which can be persisted and restored
func FromError(err error) *Error {
	if err == nil {
		return nil
	}

	// If this is already a workflow error, just return it, do not wrap again
	if e, ok := err.(*Error); ok {
		return e
	}

	e := &Error{
		Type:      getErrorType(err),
		Message:   err.Error(),
		Permanent: !CanRetry(err),
	}
	if errors.Is(err, sync.Canceled) || errors.Is(err, context.Canceled) {
		e.Code = canceledCode
	}

	if stackTracer, ok := err.(interface{ Stack() string }); ok {
		e.Stacktrace = stackTracer.Stack()
	}

	if cause := errors.Unwrap(err); cause != nil {
		e.Cause = FromError(cause)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if cause != nil {
				e.Causes = append(e.Causes, FromError(cause))
			}
		}
	}

	return e
}

// ToError attempts to convert the given workflow error into a regular error. It will create concrete errors for known error types
// and maintain the Error for unknown ones
func ToError(err *Error) error {
	if err == nil {
		return nil
	}

	e := *err
	if cause, ok := e.Cause.(*Error); ok {
		e.Cause = ToError(cause)
	}
	if len(e.Causes) > 0 {
		causes := []error{e.Cause}
		for _, cause := range e.Causes {
			causes = append(causes, ToError(cause))
		}
		e.Cause = errors.Join(causes...)
	}

	switch err.Type {
	case getErrorType(&PanicError{}):
		panicError := &PanicError{message: e.Message, stacktrace: e.Stacktrace}
		if e.Permanent {
			e.Cause = panicError
			return &e
		}
		return panicError

	default:
		// Keep *Error
		return &e
	}
}

func NewPermanentError(err error) *Error {
	e := FromError(err)
	if e == nil {
		return nil
	}
	e.Permanent = true
	return e
}

// CanRetry returns true if the given error is retryable
func CanRetry(err error) bool {
	if errors.Is(err, sync.Canceled) || errors.Is(err, context.Canceled) {
		return false
	}
	if e, ok := err.(*Error); ok && e != nil && e.Permanent {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if !CanRetry(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return CanRetry(cause)
	}

	// Retry errors by default
	return true
}
