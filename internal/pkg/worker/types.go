// Package worker is the boundary between Peen's control plane and the
// session-bound processes that actually run work.
//
// A worker is one live process for one session. It owns that session's model
// loop, tools, hooks, skills, child agents, and jobs. It never opens the
// controller's SQLite database: every durable read and write travels over the
// private, session-bound protocol in the protocol subpackage, and the
// controller writes each record before publishing it.
//
// A control worker is a child `peen worker` process that runs where the
// controller runs. A Docker worker is a psyb0t/peen container running the same
// command. Nothing here accepts a launch detail from model output or a client
// request. A client names an agent space; the operator decides what that name
// means.
package worker

import (
	"time"

	"github.com/google/uuid"
)

// Kind is the form a worker process takes.
type Kind string

const (
	// KindControl runs the worker as a child process of the controller, in
	// the control agent space.
	KindControl Kind = "control"
	// KindDocker runs the worker as a psyb0t/peen sibling container, in a
	// Docker agent space.
	KindDocker Kind = "docker"
)

// Valid reports whether the kind is one this build understands.
func (k Kind) Valid() bool {
	return k == KindControl || k == KindDocker
}

// DockerAccess is how much of the host Docker daemon a Docker agent space
// reaches.
type DockerAccess string

const (
	// DockerAccessNone gives the worker no Docker socket.
	DockerAccessNone DockerAccess = "none"
	// DockerAccessReadonly is reserved for a socket proxy that answers only
	// read endpoints. It is not available yet, and a profile naming it is
	// refused.
	DockerAccessReadonly DockerAccess = "readonly"
	// DockerAccessFull mounts the host Docker socket into the worker, which
	// is host root.
	DockerAccessFull DockerAccess = "full"
)

// Valid reports whether the access is one this build understands.
func (a DockerAccess) Valid() bool {
	switch a {
	case DockerAccessNone, DockerAccessReadonly, DockerAccessFull:
		return true
	default:
		return false
	}
}

// NetworkMode is the network a Docker agent space gives its worker.
type NetworkMode string

const (
	// NetworkModeNone gives the worker no network.
	NetworkModeNone NetworkMode = "none"
	// NetworkModeBridge puts the worker on Docker's default bridge network.
	NetworkModeBridge NetworkMode = "bridge"
	// NetworkModeHost shares the host's network stack with the worker.
	NetworkModeHost NetworkMode = "host"
)

// Valid reports whether the mode is one this build understands.
func (m NetworkMode) Valid() bool {
	switch m {
	case NetworkModeNone, NetworkModeBridge, NetworkModeHost:
		return true
	default:
		return false
	}
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

	Kind               Kind
	AgentSpace         string
	AgentSpaceRevision int64
	State              State

	// Workspace is the literal path the worker runs in. It reads the same on
	// the host, in a Docker controller, and inside a Docker worker.
	Workspace string

	// SocketPath is the private session-bound Unix socket this generation
	// talks to its controller over.
	SocketPath string

	// ProcessID is the operating system PID of a control worker. It is zero for
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
