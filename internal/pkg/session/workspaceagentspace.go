package session

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// WorkspaceAgentSpace reports the agent space the sessions of one canonical
// workspace share. found is false while the workspace has no session yet.
func (s *Store) WorkspaceAgentSpace(
	ctx context.Context,
	canonicalWorkspace string,
) (string, bool, error) {
	return currentWorkspaceAgentSpace(ctx, s.query, canonicalWorkspace)
}

// workspaceAgentSpaceFor decides the agent space a new session of workspace
// takes. A workspace with sessions keeps their agent space: a request naming
// none or the same one gets it, and a request naming another is refused. A
// workspace without sessions takes the requested agent space, or the default.
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

// currentWorkspaceAgentSpace reads the canonical session's agent space, or
// the oldest fresh session's when the canonical one does not exist yet.
func currentWorkspaceAgentSpace(
	ctx context.Context,
	tx *repositories.Query,
	workspace string,
) (string, bool, error) {
	session := tx.Session

	stored, err := session.WithContext(ctx).
		Where(session.Workspace.Eq(workspace)).
		Order(session.WorkspaceSlot, session.CreatedAt, session.ID).
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

// workspaceSessions lists every session sharing stored's workspace, stored
// included. A session with no workspace stands alone.
func workspaceSessions(
	ctx context.Context,
	tx *repositories.Query,
	stored *models.Session,
) ([]*models.Session, error) {
	if stored.Workspace == "" {
		return []*models.Session{stored}, nil
	}

	session := tx.Session

	siblings, err := session.WithContext(ctx).
		Where(session.Workspace.Eq(stored.Workspace)).
		Order(session.WorkspaceSlot, session.CreatedAt, session.ID).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list workspace sessions")
	}

	return siblings, nil
}
