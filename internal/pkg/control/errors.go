package control

import "errors"

var (
	// ErrWorkspacePathRefused reports a workspace file path that is not a
	// clean relative path, or that leaves the workspace through a symbolic
	// link. It is always joined with commerr.ErrPermissionDenied, so a caller
	// that only knows the generic refusal still treats it as one.
	ErrWorkspacePathRefused = errors.New(
		"workspace path is outside the workspace",
	)

	// ErrSystemSessionAgentSpace reports a request to move the system
	// session to another agent space. It is always joined with
	// commerr.ErrPermissionDenied.
	ErrSystemSessionAgentSpace = errors.New(
		"the system session stays in the control agent space",
	)
)
