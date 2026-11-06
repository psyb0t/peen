package control

import (
	"context"
	"os"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/harness"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	// SystemWorkspaceDirectory is the system session's directory under the
	// state directory. The client workspace policy refuses that tree, so no
	// client can open this path as an ordinary session.
	SystemWorkspaceDirectory = "system-agent"

	systemWorkspaceMode = 0o700
)

// OpenSystemSession returns the one system session, creating it on first use.
// This is the only code that gives a session the system root agent. It starts
// no turn, and it returns commerr.ErrPermissionDenied while PEEN_SYSTEM_AGENT
// is off.
func (r *Registry) OpenSystemSession(
	ctx context.Context,
) (api.OpenedSession, error) {
	if r.systemWorkspace == "" {
		return api.OpenedSession{}, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"the system agent is off",
		)
	}

	if err := os.MkdirAll(r.systemWorkspace, systemWorkspaceMode); err != nil {
		return api.OpenedSession{}, ctxerrors.Wrap(
			err,
			"create the system session directory",
		)
	}

	canonical, err := filepath.EvalSymlinks(r.systemWorkspace)
	if err != nil {
		return api.OpenedSession{}, ctxerrors.Wrap(
			err,
			"resolve the system session directory",
		)
	}

	opened, err := r.store.OpenWorkspace(
		ctx,
		canonical,
		session.OpenSessionOptions{
			RootAgent:  harness.SystemAgentName,
			ModelID:    r.defaultModel,
			AgentSpace: worker.AgentSpaceControl,
		},
	)
	if err != nil {
		return api.OpenedSession{}, ctxerrors.Wrap(
			err,
			"open the system session",
		)
	}

	ctxscope.GetLogger(ctx).Info(
		"system session opened",
		"session_id", opened.Session.ID.String(),
		"created", opened.Created,
	)

	return api.OpenedSession{
		Created: opened.Created,
		Session: agent.SessionToAPI(
			opened.Session,
			r.store.IsActive(opened.Session.ID),
		),
	}, nil
}
