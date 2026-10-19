package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// CompleteExecutionActivity commits the result before the workflow ack.
// An identical repeat is harmless. A stale generation or attempt is refused.
func (s *Store) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
	input CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	receipt, err := s.CompleteExecutionActivityReceipt(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "complete execution activity")
	}

	return receipt.Activity, nil
}

// CompleteExecutionActivityReceipt returns only events committed by this call.
// Repeated completions return the saved activity without new events.
func (s *Store) CompleteExecutionActivityReceipt(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
	input CompleteActivityInput,
) (*ExecutionActivityCompletion, error) {
	if err := s.validateActivityResult(input); err != nil {
		return nil, err
	}

	var (
		notifyIdle bool
		idleState  models.TurnState
	)

	// Deferred before the conditional activeMu lock below, so it unwinds
	// after that lock's own deferred unlock: the idle callback must never
	// run while this store still holds activeMu.
	defer func() {
		if notifyIdle {
			s.notifyIdle(sessionID, idleState)
		}
	}()

	if input.RootProjection != nil ||
		(input.Finish != nil && input.Finish.Turn != nil) {
		s.activeMu.Lock()
		defer s.activeMu.Unlock()
	}

	receipt, released, err := s.completeExecutionActivityTransaction(
		ctx, sessionID, lease, input,
	)
	if err != nil {
		return nil, err
	}

	if released {
		notifyIdle = true
		idleState = input.Finish.Turn.State
	}

	ctxscope.GetLogger(ctx).Info("execution activity completed",
		"session_id", sessionID, "execution_id", receipt.Activity.ExecutionID,
		"activity_id", receipt.Activity.ID, "attempt", receipt.Activity.Attempt)

	return receipt, nil
}

// completeExecutionActivityTransaction commits the activity result and, when
// it finishes the execution's root turn, drops that turn's lease. released
// reports whether this call was the one that dropped it, so the caller fires
// its idle notification only once, after it releases activeMu.
func (s *Store) completeExecutionActivityTransaction(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
	input CompleteActivityInput,
) (*ExecutionActivityCompletion, bool, error) {
	receipt := &ExecutionActivityCompletion{}

	var result *models.ExecutionActivity

	err := s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		var err error

		result, err = s.completeActivityWithQuery(ctx, tx, sessionID,
			lease, input, &receipt.Events)

		return err
	})
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "complete activity transaction")
	}

	released, err := s.releaseFinishedExecutionTurn(result, input)
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "release completed root turn")
	}

	receipt.Activity = result

	return receipt, released, nil
}

func (s *Store) completeActivityWithQuery(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	lease ActivityLease,
	input CompleteActivityInput,
	events *[]EventInput,
) (*models.ExecutionActivity, error) {
	activity, execution, err := s.activityForLease(ctx, tx, sessionID, lease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find activity for completion")
	}

	if activity.State == models.ActivityStateCompleted {
		return completedExecutionActivity(activity, execution, input)
	}

	if activity.State != models.ActivityStateRunning {
		return nil, ctxerrors.Wrap(
			commerr.ErrConflict, "activity is not running",
		)
	}

	if err := validateFinishClaim(activity, execution, input); err != nil {
		return nil, err
	}

	if err := checkExecutionFinishHolder(ctx, tx, activity, execution,
		input.Finish); err != nil {
		return nil, ctxerrors.Wrap(err, "fence final root callback holder")
	}

	if err := s.saveActivityResult(ctx, tx, activity, input); err != nil {
		return nil, ctxerrors.Wrap(err, "save activity completion")
	}

	if err := s.saveActivityCompaction(ctx, tx, activity, execution,
		input); err != nil {
		return nil, ctxerrors.Wrap(err, "save activity compaction")
	}

	if err := s.saveActivityProjection(ctx, tx, activity, execution,
		input, events); err != nil {
		return nil, ctxerrors.Wrap(err, "save activity projection")
	}

	if err := s.appendActivityEvent(ctx, tx, execution,
		activity, ExecutionEventTypeStepCompleted); err != nil {
		return nil, err
	}

	return activity, s.finishActivityCompletion(ctx, tx, activity,
		execution, input, events)
}

func completedExecutionActivity(
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	if err := validateProjectionRepeat(activity, input); err != nil {
		return nil, ctxerrors.Wrap(err, "reuse activity projection")
	}

	if err := validateCompactionRepeat(activity, input.Compaction); err != nil {
		return nil, ctxerrors.Wrap(err, "reuse activity compaction")
	}

	if err := validateFinishIdentity(activity, input); err != nil {
		return nil, err
	}

	if activity.ResolutionJSON != emptyActivityJSON && input.Finish != nil {
		matched, err := repeatFinishDecision(activity, input)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "reuse finish decision")
		}

		if matched {
			return activity, nil
		}
	}

	if !sameActivityResult(activity, input) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"completed activity result differs")
	}

	return activity, validateFinishedRepeat(execution, input)
}

func (s *Store) validateActivityResult(input CompleteActivityInput) error {
	if !isExecutionContext(input.Output) ||
		!isExecutionContext(input.Context) ||
		len(input.Output) > s.maxStoredMessageBytes ||
		len(input.Context) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "activity result")
	}

	if err := s.validateRootProjection(input); err != nil {
		return ctxerrors.Wrap(err, "validate root projection")
	}

	if err := s.validateChildProjection(input); err != nil {
		return ctxerrors.Wrap(err, "validate child projection")
	}

	return s.validateFinishInput(input.Finish)
}

func (s *Store) activityForLease(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	lease ActivityLease,
) (*models.ExecutionActivity, *models.Execution, error) {
	if lease.ID == uuid.Nil || lease.GenerationID == uuid.Nil ||
		lease.Attempt < 1 {
		return nil, nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"activity lease")
	}

	activity, err := s.findExecutionActivity(ctx, query, sessionID, lease.ID)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "find activity lease")
	}

	if activity.OwnerGenerationID != lease.GenerationID.String() {
		return nil, nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"activity belongs to another worker")
	}

	if activity.Attempt != lease.Attempt {
		return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"activity attempt was replaced")
	}

	execution, err := s.findExecution(
		ctx, query, sessionID, activity.ExecutionID,
	)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "find activity owner execution")
	}

	return activity, execution, nil
}

func sameActivityResult(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) bool {
	return activity.OutputJSON != nil &&
		*activity.OutputJSON == string(input.Output) &&
		activity.ContextJSON == string(input.Context)
}

func (s *Store) findExecutionActivity(
	ctx context.Context,
	query *repositories.Query,
	sessionID, activityID uuid.UUID,
) (*models.ExecutionActivity, error) {
	repository := query.ExecutionActivity

	activity, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(activityID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find session activity")
	}

	return activity, nil
}

func (s *Store) saveActivityResult(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) error {
	now := s.now()
	repository := query.ExecutionActivity

	outputJSON, err := compactColumn(ctx, query, string(input.Output))
	if err != nil {
		return ctxerrors.Wrap(err, "compact activity output")
	}

	contextJSON, err := compactColumn(ctx, query, string(input.Context))
	if err != nil {
		return ctxerrors.Wrap(err, "compact activity context")
	}

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
		repository.Attempt.Eq(activity.Attempt),
		repository.OwnerGenerationID.Eq(activity.OwnerGenerationID),
	).UpdateSimple(
		repository.State.Value(string(models.ActivityStateCompleted)),
		repository.OutputJSON.Value(outputJSON),
		repository.ContextJSON.Value(contextJSON),
		repository.CompletedAt.Value(now), repository.FailureDetail.Value(""),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "commit activity result")
	}

	output := string(input.Output)
	activity.OutputJSON = &output
	activity.ContextJSON = string(input.Context)
	activity.State = models.ActivityStateCompleted
	activity.CompletedAt = &now
	activity.FailureDetail = ""

	return nil
}

// InterruptExecutionActivity retains uncertainty instead of replaying effects.
// Model and declared retryable activities can be claimed after interruption.
func (s *Store) InterruptExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
) error {
	err := s.query.Transaction(func(tx *repositories.Query) error {
		activity, execution, err := s.activityForLease(
			ctx, tx, sessionID, lease,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "find interrupted activity")
		}

		if activity.State != models.ActivityStateRunning {
			return nil
		}

		if activity.ReplayPolicy == models.ActivityReplayPolicyReconcile {
			return s.markActivityUncertain(ctx, tx, execution, activity)
		}

		state := models.ActivityStateFailed
		kind := ExecutionEventTypeStepFailed

		if execution.State == models.ExecutionStatePaused {
			state = models.ActivityStatePaused
			kind = ExecutionEventTypeStepPaused
		}

		repository := tx.ExecutionActivity

		_, err = repository.WithContext(ctx).Where(
			repository.ID.Eq(activity.ID), repository.SessionID.Eq(sessionID),
		).UpdateSimple(
			repository.State.Value(string(state)),
			repository.FailureDetail.Value(interruptedActivityReason),
		)
		if err != nil {
			return ctxerrors.Wrap(err, "record activity interruption")
		}

		activity.State = state

		return s.appendActivityEvent(ctx, tx, execution, activity, kind)
	})
	if err != nil {
		return ctxerrors.Wrap(err, "interrupt activity transaction")
	}

	return nil
}
