package session

import (
	"errors"

	"github.com/psyb0t/ctxerrors/commerr"
)

var (
	// ErrSessionBusy reports a concurrent turn attempt for one session.
	ErrSessionBusy = commerr.ErrLockHeld
	// ErrInvalidPage reports invalid transcript pagination arguments.
	ErrInvalidPage = commerr.ErrValidationFailed

	// ErrRunningTurnSettingsChange reports a message sent while a turn runs
	// that asks for another model, reasoning level, or system prompt. A
	// queued message joins the running turn, so it cannot change how that
	// turn runs. It is always joined with commerr.ErrValidationFailed, and
	// its text is written for the person who sent the message.
	ErrRunningTurnSettingsChange = errors.New(
		"a message sent during a running turn cannot change its model, reasoning, or system prompt", //nolint:lll // One user-facing sentence.
	)
)
