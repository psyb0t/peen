package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// DeferExecutionMessage retains late input for a separate root execution.
// It requires a reserved completion and never delivers into the old model loop.
func (s *Store) DeferExecutionMessage(
	ctx context.Context,
	executionID uuid.UUID,
	lease Lease,
	input AcceptExecutionMessageInput,
) (*ExecutionInboxAcceptance, error) {
	return s.acceptExecutionInbox(ctx, executionID, lease, input,
		models.InboxMessageStateDeferred)
}

func validateDeferredBoundary(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	seal, err := readExecutionCompletionSeal(ctx, query, execution)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred completion boundary")
	}

	if seal == nil || execution.State != models.ExecutionStateRunning {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred input requires a reserved active completion")
	}

	repository := query.ExecutionActivity

	hook, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.ID.Eq(seal.ActivityID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred completion hook")
	}

	if !isCompletionSealActivity(execution, hook) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred boundary belongs to another activity")
	}

	if _, err := validateRepeatedCompletionSeal(seal, hook); err != nil {
		return ctxerrors.Wrap(err, "validate deferred completion input")
	}

	return nil
}
