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
	startCtx := context.WithoutCancel(ctx)

	ctx, cancel := context.WithTimeout(startCtx, s.options.StopTimeout)
	defer cancel()

	release, err := s.lifecycle.Acquire(ctx, sessionID)
	if err != nil {
		ctxscope.GetLogger(ctx).Error(
			"serialize disconnected worker cleanup", "err", err,
		)

		return
	}

	// A worker that Stop already removed was ended on purpose, so only a
	// generation still held here is a lost one.
	lost := s.holdsGeneration(sessionID, generationID)

	err = s.settleDisconnectedWorker(ctx, sessionID, generationID)

	release()

	if err != nil {
		ctxscope.GetLogger(ctx).Error("settle disconnected worker", "err", err)

		return
	}

	if lost {
		s.resumeAfterDisconnect(startCtx, sessionID)
	}
}

// holdsGeneration reports whether the supervisor still holds the session's
// worker, and the named generation of it unless generationID is uuid.Nil.
func (s *Supervisor) holdsGeneration(
	sessionID, generationID uuid.UUID,
) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	held, found := s.sessions[sessionID]

	return found &&
		(generationID == uuid.Nil || held.generationID == generationID)
}

// resumeAfterDisconnect starts a replacement worker when the controller
// resumes work after a crash and the session still has unfinished executions.
// Nothing else would start it: a worker is otherwise launched only when a
// message or wake reaches the session. The lifecycle lock is already released,
// because Ensure takes it.
func (s *Supervisor) resumeAfterDisconnect(
	ctx context.Context,
	sessionID uuid.UUID,
) {
	if !s.options.ResumeAfterCrash {
		return
	}

	logger := ctxscope.GetLogger(ctx)

	unfinished, err := s.options.Store.
		ListSessionsWithUnfinishedExecutions(ctx)
	if err != nil {
		logger.Warn(
			"listing unfinished work after a worker disconnected failed",
			"reason", "crash_resume_list_failed",
			"session_id", sessionID.String(),
			"err", err,
		)

		return
	}

	for _, stored := range unfinished {
		if stored.ID != sessionID {
			continue
		}

		if _, err := s.EnsureSessionWorker(
			ctx, stored.ID, stored.Workspace, stored.AgentSpace,
		); err != nil {
			logger.Warn(
				"starting a replacement worker for unfinished work failed",
				"reason", "crash_resume_worker_failed",
				"session_id", sessionID.String(),
				"err", err,
			)
		}

		return
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
