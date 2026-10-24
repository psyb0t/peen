package supervisor

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// WorkerDisconnected settles only the generation whose socket ended.
// Lifecycle admission prevents its socket cleanup deleting a replacement.
func (s *Supervisor) WorkerDisconnected(
	ctx context.Context,
	sessionID, generationID uuid.UUID,
) {
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), s.options.StopTimeout,
	)
	defer cancel()

	release, err := s.lifecycle.Acquire(ctx, sessionID)
	if err != nil {
		ctxscope.GetLogger(ctx).Error(
			"serialize disconnected worker cleanup", "err", err,
		)

		return
	}
	defer release()

	err = s.settleDisconnectedWorker(ctx, sessionID, generationID)
	if err != nil {
		ctxscope.GetLogger(ctx).Error("settle disconnected worker", "err", err)
	}
}

func (s *Supervisor) settleDisconnectedWorker(
	ctx context.Context,
	sessionID, generationID uuid.UUID,
) error {
	s.mutex.Lock()
	held, found := s.sessions[sessionID]
	s.mutex.Unlock()

	if !found {
		return nil
	}

	isOtherGeneration := generationID != uuid.Nil &&
		held.generationID != generationID
	if isOtherGeneration {
		return nil
	}

	if held.listener != nil {
		if _, connected := held.listener.Connected(); connected {
			return nil
		}
	}

	if err := s.reapDisconnectedWorker(ctx, held); err != nil {
		return ctxerrors.Wrap(err, "reap disconnected worker")
	}

	if err := s.releaseDisconnectedWork(ctx, sessionID, held); err != nil {
		return err
	}

	if err := s.options.Store.UpdateWorkerGenerationState(
		ctx, held.generationID,
		string(worker.StateFailed), "worker connection ended",
	); err != nil {
		return ctxerrors.Wrap(err, "record disconnected worker generation")
	}

	s.mutex.Lock()
	if s.sessions[sessionID] == held {
		delete(s.sessions, sessionID)
	}
	s.mutex.Unlock()
	ctxscope.GetLogger(ctx).Warn(
		"session worker disconnected",
		"reason", "worker_connection_ended",
		"generation_id", held.generationID.String(),
	)

	return nil
}

// releaseDisconnectedWork ends the turns a lost worker held. Unless the
// controller resumes after a crash, the session's unfinished executions stay
// paused until a turn resumes them.
func (s *Supervisor) releaseDisconnectedWork(
	ctx context.Context,
	sessionID uuid.UUID,
	held *supervised,
) error {
	if _, err := s.options.Store.RecoverWorkerTurns(
		ctx, sessionID, held.generationID,
	); err != nil {
		return ctxerrors.Wrap(err, "release disconnected worker turns")
	}

	if s.options.ResumeAfterCrash {
		return nil
	}

	if _, err := s.options.Store.PauseUnfinishedExecutions(
		ctx, sessionID,
	); err != nil {
		return ctxerrors.Wrap(err, "pause disconnected worker's work")
	}

	return nil
}

func (s *Supervisor) reapDisconnectedWorker(
	ctx context.Context,
	held *supervised,
) error {
	if held.process != nil {
		if err := held.process.Stop(ctx); err != nil {
			return ctxerrors.Wrap(err, "stop disconnected worker process")
		}

		s.recordExit(ctx, held)
	}

	if held.listener != nil {
		held.listener.Close()
	}

	return nil
}
