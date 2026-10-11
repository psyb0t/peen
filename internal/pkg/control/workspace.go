// Package control owns the control surface's workspace policy and session
// registry. It decides which host directories a client may open as a session
// and resolves each request to the one canonical path that identifies it.
package control

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// parentRelativePrefix is what filepath.Rel returns when the target sits
// outside the base directory rather than beneath it.
const parentRelativePrefix = ".."

// WorkspacePolicy admits only directories beneath an operator-configured root.
// It is the single place a client-supplied path becomes a host path, so the
// control surface never opens a directory the deployment did not allow.
type WorkspacePolicy struct {
	roots []string

	// protected holds canonical paths. A workspace may not equal or contain
	// any of them, and may not sit inside a state directory.
	protected ProtectedDirectories
}

// ProtectedDirectories are the controller's own directories, which no
// workspace may expose.
//
// A workspace is bind-mounted into a Docker worker. If a root such as $HOME
// holds the controller's directories, opening that root or a directory between
// it and them would hand one session every other session's data.
type ProtectedDirectories struct {
	// State directories hold controller-only data: SQLite with every
	// transcript, audit logs, and worker sockets. A workspace may not equal,
	// contain, or sit inside one.
	State []string

	// Configuration directories reach every worker read-only already. A
	// workspace may not equal or contain one, because a workspace mount is
	// writable and would let one session rewrite the harness of every other.
	Configuration []string
}

// WorkspacePolicyOption changes how NewWorkspacePolicy builds a policy.
type WorkspacePolicyOption func(*ProtectedDirectories)

// WithProtectedDirectories refuses workspaces that would expose directories.
// Empty paths are ignored, so a caller can pass optional settings as they are.
// Every other path must be absolute. It is resolved through its symlinks when
// the policy is built, and a part that does not exist yet is kept literally.
func WithProtectedDirectories(
	directories ProtectedDirectories,
) WorkspacePolicyOption {
	return func(protected *ProtectedDirectories) {
		protected.State = append(protected.State, directories.State...)
		protected.Configuration = append(
			protected.Configuration,
			directories.Configuration...,
		)
	}
}

// NewWorkspacePolicy validates the operator's roots once at startup. Every root
// must already be absolute and canonical, which config.WorkspaceRoots
// guarantees, so a later request compares against a stable path.
func NewWorkspacePolicy(
	roots []string,
	options ...WorkspacePolicyOption,
) (*WorkspacePolicy, error) {
	if len(roots) == 0 {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"at least one workspace root is required",
		)
	}

	owned := make([]string, 0, len(roots))

	for index, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"workspace root %d is not absolute",
				index,
			)
		}

		owned = append(owned, filepath.Clean(root))
	}

	requested := ProtectedDirectories{}
	for _, option := range options {
		option(&requested)
	}

	state, err := canonicalProtectedDirectories(requested.State)
	if err != nil {
		return nil, err
	}

	configuration, err := canonicalProtectedDirectories(
		requested.Configuration,
	)
	if err != nil {
		return nil, err
	}

	return &WorkspacePolicy{
		roots: owned,
		protected: ProtectedDirectories{
			State:         state,
			Configuration: configuration,
		},
	}, nil
}

func canonicalProtectedDirectories(directories []string) ([]string, error) {
	canonical := make([]string, 0, len(directories))

	for _, directory := range directories {
		if directory == "" {
			continue
		}

		if !filepath.IsAbs(directory) {
			return nil, ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"protected directory %q is not absolute",
				directory,
			)
		}

		resolved, err := canonicalDirectory(directory)
		if err != nil {
			return nil, err
		}

		canonical = append(canonical, resolved)
	}

	return canonical, nil
}

// canonicalDirectory resolves directory through its symlinks.
//
// The worker socket root may not exist yet when the policy is built, so the
// deepest existing ancestor is resolved and the missing parts are joined back
// on literally. Any failure other than a missing part refuses, because an
// unreadable component is exactly where a link could hide.
func canonicalDirectory(directory string) (string, error) {
	current := filepath.Clean(directory)

	var missing []string

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", ctxerrors.Wrapf(
				err,
				"resolve protected directory %q",
				directory,
			)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", ctxerrors.Wrapf(
				err,
				"resolve protected directory %q",
				directory,
			)
		}

		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

// Roots returns the configured roots for reporting. The copy keeps a caller
// from rewriting the policy it is only meant to read.
func (p *WorkspacePolicy) Roots() []string {
	roots := make([]string, len(p.roots))
	copy(roots, p.roots)

	return roots
}

// Resolve turns a requested workspace into the canonical path that identifies
// its session. It returns commerr.ErrNotFound when an admitted path does not
// exist, commerr.ErrValidationFailed for a path that is not a usable directory,
// and commerr.ErrPermissionDenied for one outside every configured root.
//
// The path is resolved through its symlinks before the root check, so an alias
// of an allowed directory is admitted and resolves to the same session as the
// real path. Resolving first is also what stops a symlink inside an allowed
// root from reaching a directory outside it.
func (p *WorkspacePolicy) Resolve(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace is required",
		)
	}

	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", ctxerrors.Wrap(err, "make workspace absolute")
	}

	// Reject an obvious path outside the policy before filesystem resolution.
	// A later check after symlink resolution is still required, because a path
	// inside a root may point outside it.
	if !p.admits(absolute) {
		return "", workspaceNotAllowed()
	}

	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", workspaceNotFound()
		}

		return "", ctxerrors.Wrap(err, "resolve workspace symlinks")
	}

	info, err := os.Stat(canonical)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", workspaceNotFound()
		}

		return "", ctxerrors.Wrap(err, "stat workspace")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace is not a directory",
		)
	}

	if err := p.refuseCanonical(canonical); err != nil {
		return "", err
	}

	return canonical, nil
}

// refuseCanonical applies the policy to a resolved workspace. Neither refusal
// names a directory, because the caller is not entitled to learn the
// deployment's layout or where the controller keeps its state.
func (p *WorkspacePolicy) refuseCanonical(canonical string) error {
	if !p.admits(canonical) {
		return workspaceNotAllowed()
	}

	if p.exposesProtected(canonical) {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"workspace would expose the controller's own directories",
		)
	}

	return nil
}

// exposesProtected reports whether a workspace at canonical would reach a
// protected directory: it equals or contains any of them, or sits inside a
// state directory.
func (p *WorkspacePolicy) exposesProtected(canonical string) bool {
	for _, state := range p.protected.State {
		if isSameOrBeneath(canonical, state) ||
			isSameOrBeneath(state, canonical) {
			return true
		}
	}

	for _, configuration := range p.protected.Configuration {
		if isSameOrBeneath(canonical, configuration) {
			return true
		}
	}

	return false
}

func workspaceNotFound() error {
	return ctxerrors.Wrap(
		commerr.ErrNotFound,
		"workspace directory does not exist",
	)
}

func workspaceNotAllowed() error {
	return ctxerrors.Wrap(
		commerr.ErrPermissionDenied,
		"workspace is outside every configured workspace root",
	)
}

// admits reports whether canonical is a configured root or sits beneath one.
// The comparison walks path components through filepath.Rel rather than
// matching a string prefix, so /srv/work never admits /srv/work-other.
func (p *WorkspacePolicy) admits(canonical string) bool {
	for _, root := range p.roots {
		if isSameOrBeneath(root, canonical) {
			return true
		}
	}

	return false
}

// isSameOrBeneath reports whether target is base or sits beneath it, comparing
// path components rather than string prefixes.
func isSameOrBeneath(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}

	if relative == "." {
		return true
	}

	escapePrefix := parentRelativePrefix + string(filepath.Separator)
	if relative == parentRelativePrefix ||
		strings.HasPrefix(relative, escapePrefix) {
		return false
	}

	return !filepath.IsAbs(relative)
}
