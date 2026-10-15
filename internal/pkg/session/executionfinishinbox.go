package session

import (
	"context"
	"errors"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func requireSettledExecutionInbox(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	terminal models.ExecutionState,
) error {
	if terminal != models.ExecutionStateCompleted {
		return nil
	}

	cutoff, err := completedHookInboxWatermark(ctx, query, execution)
	if err != nil {
		return ctxerrors.Wrap(err, "read completion inbox boundary")
	}

	count, err := query.ExecutionInboxMessage.WithContext(ctx).
		CompletionBlockers(execution.SessionID.String(), execution.ID.String(),
			string(models.InboxMessageStateQueued), cutoff,
			EventTypeAgentRunCompleted, EventTypeAgentRunFailed)
	if err != nil {
		return ctxerrors.Wrap(err, "count pending execution inbox")
	}

	if count != 0 {
		return ctxerrors.Wrap(errors.Join(commerr.ErrConflict,
			ErrExecutionInboxPending),
			"execution still has pending inbox messages")
	}

	return nil
}

// Only reports after a committed hook boundary belong to a later decision.
func completedHookInboxWatermark(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) (int64, error) {
	seal, err := readExecutionCompletionSeal(ctx, query, execution)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read sealed completion history")
	}

	if seal == nil {
		return math.MaxInt64, nil
	}

	repository := query.ExecutionActivity

	hook, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.ID.Eq(seal.ActivityID),
	).First()
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read sealed terminal hook")
	}

	if !isCompletionSealActivity(execution, hook) ||
		hook.State != models.ActivityStateCompleted {
		return 0, ctxerrors.Wrap(commerr.ErrConflict,
			"completion boundary requires a completed terminal hook")
	}

	if _, err := validateRepeatedCompletionSeal(seal, hook); err != nil {
		return 0, ctxerrors.Wrap(err, "validate sealed terminal hook input")
	}

	return seal.InboxSequence, nil
}
