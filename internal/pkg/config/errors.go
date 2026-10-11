package config

import "errors"

var (
	// ErrInvalidConfig reports invalid deployment configuration.
	ErrInvalidConfig = errors.New("invalid Peen configuration")
	// ErrInvalidUpstream reports malformed named LLM provider configuration.
	ErrInvalidUpstream = errors.New("invalid Peen upstream")
	// ErrControlSpaceRefused reports the control agent space offered beside a
	// reachable Docker socket. A control worker has the controller's own user
	// and groups, so it can use that socket to start a container that mounts
	// the host.
	ErrControlSpaceRefused = errors.New(
		"the control agent space is refused beside a Docker socket",
	)
)
