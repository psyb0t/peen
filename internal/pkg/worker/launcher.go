package worker

import "context"

// Launcher starts one worker process for one session.
//
// Native and Docker launching differ only here. Everything above this point,
// the generation record, the credential, the socket, and the protocol, is the
// same for both, so a new launch form does not change the supervisor.
type Launcher interface {
	// Kind is the profile kind this launcher serves.
	Kind() Kind

	// Launch starts the worker and returns once the process or container
	// exists. It does not wait for the worker to register: the supervisor
	// waits for that on the protocol socket, which is the only proof the
	// worker is actually usable.
	Launch(ctx context.Context, request LaunchRequest) (Process, error)
}

// LaunchRequest is one fully resolved worker launch.
//
// Every field comes from the controller. Nothing here is taken from model
// output or a client request.
type LaunchRequest struct {
	Document LaunchDocument
	Profile  Profile
}

// Process is a launched worker as its launcher holds it.
type Process interface {
	// Describe reports the worker's operating system or container identity.
	Describe() Descriptor

	// Stop asks the worker to end and waits for it within the context.
	Stop(ctx context.Context) error

	// Wait blocks until the worker ends and returns its exit code.
	Wait() (int, error)
}

// Descriptor is a launched worker's recorded identity.
type Descriptor struct {
	// ProcessID is set for a native worker.
	ProcessID int

	// ContainerID and ImageDigest are set for a Docker worker. The digest is
	// immutable image identity, never a moving tag.
	ContainerID string
	ImageDigest string
}
