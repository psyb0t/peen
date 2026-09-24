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
}

// NewWorkspacePolicy validates the operator's roots once at startup. Every root
// must already be absolute and canonical, which config.WorkspaceRoots
// guarantees, so a later request compares against a stable path.
func NewWorkspacePolicy(roots []string) (*WorkspacePolicy, error) {
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

	return &WorkspacePolicy{roots: owned}, nil
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

	if !p.admits(canonical) {
		// The refusal names no root, because the caller is not entitled to
		// learn the deployment's directory layout from a rejected request.
		return "", workspaceNotAllowed()
	}

	return canonical, nil
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
		relative, err := filepath.Rel(root, canonical)
		if err != nil {
			continue
		}

		if relative == "." {
			return true
		}

		escapePrefix := parentRelativePrefix + string(filepath.Separator)
		if relative == parentRelativePrefix ||
			strings.HasPrefix(relative, escapePrefix) {
			continue
		}

		if filepath.IsAbs(relative) {
			continue
		}

		return true
	}

	return false
}
