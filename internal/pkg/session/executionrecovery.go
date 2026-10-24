package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// ExecutionCrashPauseReason is recorded on work paused because the process
// that ran it died.
const ExecutionCrashPauseReason = "paused after a crash"

// PauseUnfinishedExecutions pauses, with their subtrees, the executions a dead
// process left running or reconciling, so nothing continues until something
// resumes it explicitly. Queued executions have not started and stay queued.
// sessionID limits it to one session; uuid.Nil means every session. It returns
// how many executions it paused.
func (s *Store) PauseUnfinishedExecutions(
	ctx context.Context,
	sessionID uuid.UUID,
) (int, error) {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: ExecutionCrashPauseReason})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "encode crash pause")
	}

	paused := 0

	err = s.query.Transaction(func(tx *repositories.Query) error {
		repository := tx.Execution
		query := repository.WithContext(ctx).Where(repository.State.In(
			string(models.ExecutionStateRunning),
			string(models.ExecutionStateReconciling),
		))

		if sessionID != uuid.Nil {
			query = query.Where(repository.SessionID.Eq(sessionID))
		}

		unfinished, err := query.Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list unfinished executions")
		}

		for _, execution := range unfinished {
			changed, err := s.changeExecutionSubtree(ctx, tx,
				execution.SessionID, execution.ID, ExecutionEventTypePaused,
				ExecutionCrashPauseReason, payload)
			if err != nil {
				return ctxerrors.Wrap(err, "pause unfinished execution")
			}

			paused += len(changed)
		}

		return nil
	})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "pause unfinished executions")
	}

	return paused, nil
}
