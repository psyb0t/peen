package hooks

import "errors"

var (
	ErrDenied               = errors.New("hook denied operation")
	ErrCommandOutputLimit   = errors.New("hook command output limit exceeded")
	ErrEventUnavailable     = errors.New("hook event publisher is unavailable")
	ErrInvalidContextTokens = errors.New("invalid hook context token estimate")
)

// DenialError carries the exact reason a deny action or command gave. It
// matches ErrDenied through errors.Is.
//
// The reason is model-facing text such as a project rule, so callers read it
// with errors.As rather than recovering it from a formatted error chain. A
// formatted chain adds wrap prefixes and a source location, and cutting those
// off by searching for the location marker also cuts any reason that happens
// to contain the same characters.
type DenialError struct {
	Reason string
}

func (e *DenialError) Error() string {
	return e.Reason + ": " + ErrDenied.Error()
}

func (e *DenialError) Unwrap() error {
	return ErrDenied
}
