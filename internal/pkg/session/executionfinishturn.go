package session

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func validateExecutionFinishTurn(input *FinishExecutionInput) error {
	if err := validateExecutionFinishHolder(input.RootHolder); err != nil {
		return err
	}

	if input.Turn == nil {
		return nil
	}

	if input.RootHolder == nil || !isTerminalState(input.Turn.State) ||
		(input.State == models.ExecutionStateCompleted) !=
			(input.Turn.State == models.TurnStateCompleted) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root turn terminal result")
	}

	return nil
}

// The caller holds activeMu until the completion transaction commits.
func (s *Store) finishExecutionTurn(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	input *FinishExecutionInput,
) error {
	if input.Turn == nil {
		return nil
	}

	lease := Lease{
		ID:        input.RootHolder.LeaseID,
		SessionID: execution.SessionID, TurnID: execution.RootTurnID,
	}
	if !s.hasLeaseLocked(lease) {
		return ctxerrors.Wrap(commerr.ErrConflict, "root turn lease changed")
	}

	result, err := s.appendTranscript(ctx, tx, lease,
		input.Turn.Messages, input.Turn.Events)
	if err != nil {
		return ctxerrors.Wrap(err, "append final root transcript")
	}

	if err := s.finishTurn(ctx, tx, lease, *input.Turn); err != nil {
		return ctxerrors.Wrap(err, "save final root turn")
	}

	if err := s.markUnfinishedMessages(ctx, tx, lease,
		*input.Turn); err != nil {
		return ctxerrors.Wrap(err, "mark final root messages")
	}

	repository := tx.Message

	latest, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(lease.SessionID),
		repository.TurnID.Eq(lease.TurnID),
	).Order(repository.Sequence.Desc()).Limit(1).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read final root transcript sequence")
	}

	if len(latest) != 0 {
		result.lastMessageSequence = latest[0].Sequence
	}

	return s.updateSessionForCompletion(ctx, tx, lease.SessionID,
		input.Turn.State, result)
}

func (s *Store) releaseFinishedExecutionTurn(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) error {
	if input.Finish == nil || input.Finish.Turn == nil {
		return nil
	}

	continued, err := repeatFinishDecision(activity, input)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed root finish decision")
	}

	if continued {
		return nil
	}

	active, ok := s.active[activity.SessionID]
	if ok && active.leaseID == input.Finish.RootHolder.LeaseID {
		delete(s.active, activity.SessionID)
	}

	return nil
}
