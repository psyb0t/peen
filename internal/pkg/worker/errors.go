package worker

import "errors"

// ErrDockerAuthorityUnavailable reports a Docker agent space the controller
// cannot launch because it has no Docker socket of its own.
//
// Peen never falls back to the control agent space when this happens. A Docker
// agent space is an operator's statement about isolation, and quietly running
// the session somewhere else would break that statement without telling
// anyone.
var ErrDockerAuthorityUnavailable = errors.New(
	"the controller has no Docker socket, so Docker agent spaces cannot launch",
)

// ErrAgentSpaceNotAllowed reports a requested agent space the deployment does
// not offer. A refusal wraps it together with commerr.ErrPermissionDenied, so a
// caller can tell it apart from a workspace the policy refused.
var ErrAgentSpaceNotAllowed = errors.New(
	"the agent space is not allowed by this deployment",
)

// ErrDockerReadonlyUnavailable reports a Docker profile that asks for
// `docker: readonly`. A read-only socket proxy does not exist yet, so the
// profile is refused instead of quietly getting more or less access than it
// named.
var ErrDockerReadonlyUnavailable = errors.New(
	"docker: readonly is not available yet, use none or full",
)

// ErrWorkerLaunchFailed reports a worker that never became ready.
var ErrWorkerLaunchFailed = errors.New("worker launch failed")
