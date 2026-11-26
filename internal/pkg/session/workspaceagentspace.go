package session

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// WorkspaceAgentSpace reports the agent space for a workspace session.
func (s *Store) WorkspaceAgentSpace(
	ctx context.Context,
	canonicalWorkspace string,
) (string, bool, error) {
	return currentWorkspaceAgentSpace(ctx, s.query, canonicalWorkspace)
}

// workspaceAgentSpaceFor decides the agent space a new session of workspace
// takes. An existing session keeps its space and refuses a different one.
func workspaceAgentSpaceFor(
	ctx context.Context,
	tx *repositories.Query,
	workspace string,
	options OpenSessionOptions,
) (string, error) {
	current, found, err := currentWorkspaceAgentSpace(ctx, tx, workspace)
	if err != nil {
		return "", err
	}

	if !found {
		if options.AgentSpace != "" {
			return options.AgentSpace, nil
		}

		return options.DefaultAgentSpace, nil
	}

	if options.AgentSpace == "" || options.AgentSpace == current {
		return current, nil
	}

	return "", ctxerrors.Wrap(
		&AgentSpaceConflictError{
			Current:   current,
			Requested: options.AgentSpace,
		},
		"workspace agent space",
	)
}

// currentWorkspaceAgentSpace reads the workspace session's agent space.
func currentWorkspaceAgentSpace(
	ctx context.Context,
	tx *repositories.Query,
	workspace string,
) (string, bool, error) {
	session := tx.Session

	stored, err := session.WithContext(ctx).
		Where(session.Workspace.Eq(workspace)).
		First()
	if err == nil {
		return stored.AgentSpace, true, nil
	}

	err = ctxerrors.Wrap(err, "query workspace agent space")
	if errors.Is(err, commerr.ErrNotFound) {
		return "", false, nil
	}

	return "", false, err
}
