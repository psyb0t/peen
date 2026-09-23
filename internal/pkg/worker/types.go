// Package worker is the boundary between Peen's control plane and the
// session-bound processes that actually run work.
//
// A worker is one live process for one session. It owns that session's model
// loop, tools, hooks, skills, child agents, and jobs. It never opens the
// controller's SQLite database: every durable read and write travels over the
// private, session-bound protocol in the protocol subpackage, and the
// controller writes each record before publishing it.
//
// A native worker is a child `peen worker` process. A Docker worker is a
// psyb0t/peen container running the same command. Nothing here accepts a
// launch detail from model output or a client request. A client names a
// profile; the operator decides what that name means.
package worker

import (
	"time"

	"github.com/google/uuid"
)

// Kind is the form a worker process takes.
type Kind string

const (
	// KindNative runs the worker as a child process of the controller.
	KindNative Kind = "native"
	// KindDocker runs the worker as a psyb0t/peen sibling container.
	KindDocker Kind = "docker"
)

// Valid reports whether the kind is one this build understands.
func (k Kind) Valid() bool {
	return k == KindNative || k == KindDocker
}

// State is one point in a worker generation's durable lifecycle.
type State string

const (
	// StateRequested means the generation is recorded but nothing has launched.
	StateRequested State = "requested"
	// StateStarting means the process or container is coming up.
	StateStarting State = "starting"
	// StateReady means the worker registered over the protocol and may run
	// work for its session.
	StateReady State = "ready"
	// StateStopping means the worker was asked to finish and exit.
	StateStopping State = "stopping"
	// StateStopped means the worker ended as asked.
	StateStopped State = "stopped"
	// StateFailed means the worker ended without being asked to, or never
	// became ready.
	StateFailed State = "failed"
)

// Valid reports whether the state is one this build understands.
func (s State) Valid() bool {
	switch s {
	case StateRequested, StateStarting, StateReady,
		StateStopping, StateStopped, StateFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether a generation in this state can no longer run work.
func (s State) Terminal() bool {
	return s == StateStopped || s == StateFailed
}

// Generation is the durable record of one worker process.
//
// It exists before the process launches, so a controller that dies mid-launch
// still leaves a row an operator can reconcile.
type Generation struct {
	ID        uuid.UUID
	SessionID uuid.UUID

	Kind            Kind
	Profile         string
	ProfileRevision int64
	State           State

	// Workspace is the literal path the worker runs in. It reads the same on
	// the host, in a Docker controller, and inside a Docker worker.
	Workspace string

	// SocketPath is the private session-bound Unix socket this generation
	// talks to its controller over.
	SocketPath string

	// ProcessID is the operating system PID of a native worker. It is zero for
	// a Docker worker, whose identity is its container.
	ProcessID int

	// ContainerID identifies a Docker worker. ImageDigest records immutable
	// repository identity when Docker reports one. It is empty for a local
	// image without a repository digest.
	ContainerID string
	ImageDigest string

	// FailureDetail says why a generation failed or was replaced. It never
	// carries a credential.
	FailureDetail string

	// ExitCode is the worker's process or container exit status once it ended.
	ExitCode int

	StartedAt *time.Time
	EndedAt   *time.Time
}
