package harness

import "errors"

var (
	ErrInvalidOptions       = errors.New("invalid harness resolver options")
	ErrInvalidPath          = errors.New("invalid harness path")
	ErrNotDirectory         = errors.New("harness path is not a directory")
	ErrUnreadableLayer      = errors.New("unreadable harness layer")
	ErrInvalidInstruction   = errors.New("invalid agent instructions")
	ErrInvalidSkill         = errors.New("invalid agent skill")
	ErrInvalidAgent         = errors.New("invalid named agent")
	ErrInvalidEventHandler  = errors.New("invalid event handler")
	ErrResourceLimit        = errors.New("harness resource limit exceeded")
	ErrSkillNotFound        = errors.New("skill not found")
	ErrAgentNotFound        = errors.New("agent not found")
	ErrEventHandlerNotFound = errors.New("event handler not found")

	// ErrUnsupportedHomeReference reports a "~user" path segment. Only the
	// running user's own home directory can be expanded.
	ErrUnsupportedHomeReference = errors.New(
		"named user home directory is not supported",
	)
)
