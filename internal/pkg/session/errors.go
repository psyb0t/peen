package session

import (
	"errors"

	"github.com/psyb0t/ctxerrors/commerr"
)

var (
	// ErrExecutionInboxPending requires continuation before successful finish.
	// Storage and transport return it joined with commerr.ErrConflict.
	ErrExecutionInboxPending = errors.New("execution inbox has pending work")
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

	// ErrAgentRunFinished reports a message sent to a child agent that is no
	// longer running, so nothing would ever read it. It is always joined
	// with commerr.ErrConflict.
	ErrAgentRunFinished = errors.New("agent has finished")
	// ErrAgentRunPaused reports a message sent to a child agent whose work
	// is paused, after a Stop or a crash, until something resumes it. It is
	// always joined with commerr.ErrInvalidState.
	ErrAgentRunPaused = errors.New("agent is paused")
	// ErrAgentRunNotFound reports a message sent to an agent run the session
	// never had. It is always joined with commerr.ErrNotFound.
	ErrAgentRunNotFound = errors.New("agent run not found")
)
