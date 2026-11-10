package controlcore

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

// unfinishedSessionLister lists the sessions whose work a dead process left
// running.
type unfinishedSessionLister interface {
	ListSessionsWithUnfinishedExecutions(
		ctx context.Context,
	) ([]*models.Session, error)
}

// sessionWorkerEnsurer starts a session's worker when it has none.
type sessionWorkerEnsurer interface {
	EnsureSessionWorker(
		ctx context.Context,
		sessionID uuid.UUID,
		workspace string,
		agentSpaceName string,
	) (*protocol.Worker, error)
}

// resumeCrashedSessions starts a worker for every session that has unfinished
// executions. A worker restores its session's admitted executions when it
// starts, so this is all it takes for work a crash interrupted to continue
// without a client sending anything. A failure is logged and does not stop the
// other sessions.
func resumeCrashedSessions(
	ctx context.Context,
	lister unfinishedSessionLister,
	workers sessionWorkerEnsurer,
) error {
	logger := ctxscope.GetLogger(ctx)

	sessions, err := lister.ListSessionsWithUnfinishedExecutions(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "list sessions with unfinished work")
	}

	for _, stored := range sessions {
		if err := ctx.Err(); err != nil {
			return ctxerrors.Wrap(err, "resume crashed work")
		}

		if _, err := workers.EnsureSessionWorker(
			ctx, stored.ID, stored.Workspace, stored.AgentSpace,
		); err != nil {
			logger.Warn(
				"starting a worker to resume crashed work failed",
				"reason", "crash_resume_worker_failed",
				"session_id", stored.ID.String(),
				"err", err,
			)

			continue
		}

		logger.Info(
			"worker started to resume crashed work",
			"session_id", stored.ID.String(),
		)
	}

	return nil
}

// startCrashResume runs resumeCrashedSessions off the startup path, so a slow
// worker launch never delays readiness. It does nothing unless the deployment
// resumes work after a crash.
func startCrashResume(
	ctx context.Context,
	isEnabled bool,
	lister unfinishedSessionLister,
	workers sessionWorkerEnsurer,
) {
	if !isEnabled {
		return
	}

	go func() {
		if err := resumeCrashedSessions(ctx, lister, workers); err != nil {
			ctxscope.GetLogger(ctx).Warn(
				"resuming crashed work failed",
				"reason", "crash_resume_failed",
				"err", err,
			)
		}
	}()
}
