package hooks

import "errors"

var (
	ErrDenied             = errors.New("hook denied operation")
	ErrCommandOutputLimit = errors.New("hook command output limit exceeded")
	ErrEventUnavailable   = errors.New("hook event publisher is unavailable")
)
