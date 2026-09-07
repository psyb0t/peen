package session

import "github.com/psyb0t/ctxerrors/commerr"

var (
	// ErrSessionBusy reports a concurrent turn attempt for one session.
	ErrSessionBusy = commerr.ErrLockHeld
	// ErrInvalidPage reports invalid transcript pagination arguments.
	ErrInvalidPage = commerr.ErrValidationFailed
)
