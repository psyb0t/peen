package worker

import "errors"

// ErrDockerAuthorityUnavailable reports a Docker profile the controller cannot
// launch because it has no Docker socket of its own.
//
// Peen never falls back to host or in-container native work when this happens.
// A Docker profile is an operator's statement about isolation, and quietly
// running the session somewhere else would break that statement without
// telling anyone.
var ErrDockerAuthorityUnavailable = errors.New(
	"the controller has no Docker socket, so Docker profiles cannot launch",
)

// ErrWorkerLaunchFailed reports a worker that never became ready.
var ErrWorkerLaunchFailed = errors.New("worker launch failed")
