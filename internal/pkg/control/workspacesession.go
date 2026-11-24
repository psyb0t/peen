package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

// workspaceSession rechecks deployment policy before controller integrations
// use a stored workspace. A changed symlink cannot redirect the stored path.
func (r *Registry) workspaceSession(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Session, error) {
	stored, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load workspace session")
	}

	if stored.RootAgent == harness.SystemAgentName {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"workspace operation cannot address the system session")
	}

	canonical, err := r.policy.Resolve(stored.Workspace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate stored workspace policy")
	}

	if canonical != stored.Workspace {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"stored workspace changed its canonical path")
	}

	return stored, nil
}

// ValidateWorkspaceSession checks regular-session access under current policy.
func (r *Registry) ValidateWorkspaceSession(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	if _, err := r.workspaceSession(ctx, sessionID); err != nil {
		return ctxerrors.Wrap(err, "validate integration session")
	}

	return nil
}

// ResolveWorkspace checks deployment policy without creating a session.
func (r *Registry) ResolveWorkspace(requested string) (string, error) {
	canonical, err := r.policy.Resolve(requested)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve integration workspace")
	}

	return canonical, nil
}
