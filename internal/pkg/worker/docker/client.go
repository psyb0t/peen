// Package docker launches a session worker as a psyb0t/peen sibling container.
//
// The container runs the same `peen worker` command a native worker runs, so a
// Docker worker is the same program in a different environment rather than a
// remote tool backend.
//
// Every daemon call goes through the Client interface in this file. The
// interface is the seam a real daemon client implements, which is what lets the
// launch rules, mount construction, host identity, and ownership-safe lifecycle
// be tested without a daemon. It is not a simulation of Docker.
package docker

import (
	"context"
	"io"

	"github.com/psyb0t/peen/internal/pkg/worker"
)

// ContainerState is what the daemon reports about one container.
type ContainerState struct {
	ID string

	// Running distinguishes a container still serving its session from one
	// that exited while the controller was away.
	Running bool

	// ExitCode is meaningful only when Running is false.
	ExitCode int

	// ImageID is the daemon's local content ID for the image this container
	// runs. It is not a repository digest, and the launcher does not record it
	// as one.
	ImageID string

	// Labels carry the ownership marks the controller checks before acting.
	Labels map[string]string
}

// ImageIdentity is what the daemon reports about one image.
//
// ID is the daemon's local content ID. It is deliberately separate from
// RepoDigests, because a local ID is not a registry digest and recording one in
// the other's place would claim an identity nobody can resolve later.
type ImageIdentity struct {
	ID string

	// RepoDigests are the immutable repository references this image is known
	// by, in `repository@sha256:...` form. An image built locally and never
	// pushed or pulled reports none.
	RepoDigests []string
}

// CreateRequest is one fully resolved worker container launch.
//
// Every field comes from operator configuration and the controller's own host
// identity. Nothing here is taken from model output or a client request.
type CreateRequest struct {
	// Name is deterministic: peen-worker-<session-uuid>.
	Name string

	Image string

	// Entrypoint overrides the image's own entrypoint. BuildCreateRequest never
	// sets it, so a worker always runs the image's Peen entrypoint and its
	// privilege drop. It exists for the image-entrypoint contract test, which
	// keeps that entrypoint and replaces only the program it finally execs.
	Entrypoint []string

	// Command is the worker subcommand. The image never decides what to run.
	Command []string

	Mounts []worker.Mount

	// User is root only while the image entrypoint reconciles the host account
	// and drops to it. The agent itself never runs as this user.
	User string

	// Groups are supplementary group IDs the worker needs, such as the Docker
	// socket's group when the profile mounts it.
	Groups []string

	// Env carries HOME, USER, LOGNAME, the configuration directory, and the
	// launch document path. It never carries the credential itself.
	Env map[string]string

	// WorkingDirectory is the session workspace at its literal host path.
	WorkingDirectory string

	// NetworkDisabled asks the daemon for an isolated network.
	NetworkDisabled bool

	// NoNewPrivileges prevents a non-escalating worker from gaining privilege
	// after the entrypoint has dropped to the host account.
	NoNewPrivileges bool

	// Labels are exactly peen.managed and peen.session.
	Labels map[string]string
}

// Client is the daemon surface this package needs.
//
// It is deliberately small. Every method here is one the launcher actually
// calls, so a fake implements exactly the daemon behavior under test.
type Client interface {
	// CreateContainer creates without starting and returns the container ID.
	CreateContainer(
		ctx context.Context,
		request CreateRequest,
	) (string, error)

	// AttachStdin writes the launch document to the created container's stdin,
	// which is how a Docker worker receives its credential without it ever
	// appearing in the container's command line or environment.
	AttachStdin(
		ctx context.Context,
		containerID string,
		document []byte,
	) error

	// PullImage fetches a reference the daemon does not already hold.
	//
	// The Engine API's container create does not pull, unlike the docker CLI,
	// which pulls on a 404 and retries. A controller that never pulls cannot
	// start a worker on a host that has not seen the image before.
	PullImage(ctx context.Context, reference string) error

	// StreamLogs follows one container's combined stdout and stderr until the
	// container exits or the reader is closed. The worker's records reach the
	// controller's own sinks this way, because a worker never receives the
	// audit directory and cannot write to it itself.
	StreamLogs(
		ctx context.Context,
		containerID string,
	) (io.ReadCloser, error)

	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string) error
	InspectContainer(
		ctx context.Context,
		containerID string,
	) (ContainerState, error)

	// InspectImage reports one image's local ID and its repository digests.
	// The launcher uses it to record what actually ran, which a container
	// inspect cannot give: that call returns a local image ID, not a digest.
	InspectImage(
		ctx context.Context,
		reference string,
	) (ImageIdentity, error)
}
