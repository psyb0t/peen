package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type ExecutionCompletionSeal struct {
	ActivityID      uuid.UUID `json:"activityId"`
	InputHash       string    `json:"inputHash"`
	InboxSequence   int64     `json:"inboxSequence"`
	ControlRevision int64     `json:"controlRevision"`
}

// ReserveExecutionCompletion retains a terminal hook's empty-inbox boundary.
// A nil result requires continuation. Repeats retain the saved marker.
func (s *Store) ReserveExecutionCompletion(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
) (*ExecutionCompletionSeal, error) {
	var result *ExecutionCompletionSeal

	err := s.query.Transaction(func(tx *repositories.Query) error {
		var err error

		result, err = s.reserveExecutionCompletion(ctx, tx, sessionID, lease)

		return err
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "reserve execution completion")
	}

	ctxscope.GetLogger(ctx).Debug("execution completion boundary checked",
		"session_id", sessionID, "activity_id", lease.ID,
		"reserved", result != nil)

	return result, nil
}

//nolint:nilnil // Pending input requires continuation without a seal.
func (s *Store) reserveExecutionCompletion(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	lease ActivityLease,
) (*ExecutionCompletionSeal, error) {
	activity, execution, err := s.activityForLease(ctx, query, sessionID, lease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate completion reservation claim")
	}

	if !isCompletionSealActivity(execution, activity) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"activity cannot reserve root completion")
	}

	seal, err := readExecutionCompletionSeal(ctx, query, execution)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read completion reservation")
	}

	if seal != nil {
		return validateRepeatedCompletionSeal(seal, activity)
	}

	if err := validateCompletionReservation(ctx, query, execution,
		activity, lease); err != nil {
		return nil, ctxerrors.Wrap(err, "validate new completion reservation")
	}

	if err := requireSettledExecutionInbox(ctx, query, execution,
		models.ExecutionStateCompleted); err != nil {
		if errors.Is(err, ErrExecutionInboxPending) {
			return nil, nil
		}

		return nil, ctxerrors.Wrap(err, "check completion reservation inbox")
	}

	if err := requireOtherCompletionActivities(ctx, query, execution,
		activity); err != nil {
		return nil, ctxerrors.Wrap(err, "settle completion reservation work")
	}

	return s.createExecutionCompletionSeal(ctx, query, execution, activity)
}

func isCompletionSealActivity(
	execution *models.Execution,
	activity *models.ExecutionActivity,
) bool {
	return execution.Kind == models.ExecutionKindRoot &&
		activity.Name == ExecutionTerminalHooksActivityName &&
		activity.Kind == models.ActivityKindTool &&
		activity.ReplayPolicy == models.ActivityReplayPolicyReconcile
}

func requireOtherCompletionActivities(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
) error {
	repository := query.ExecutionActivity

	count, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.ID.Neq(activity.ID),
		repository.State.Neq(string(models.ActivityStateCompleted)),
	).Count()
	if err != nil {
		return ctxerrors.Wrap(err, "count unfinished completion work")
	}

	if count != 0 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"completion reservation has unfinished activities")
	}

	return nil
}

func validateCompletionReservation(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	lease ActivityLease,
) error {
	if execution.State != models.ExecutionStateRunning ||
		activity.State != models.ActivityStateRunning {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"completion reservation requires an active claim")
	}

	holder, err := findExecutionTurnLease(ctx, query, execution)
	if err != nil {
		return ctxerrors.Wrap(err, "read completion reservation holder")
	}

	if holder.OwnerGenerationID != lease.GenerationID ||
		holder.ControlRevision != execution.ControlRevision {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"completion reservation holder changed")
	}

	return nil
}

//nolint:nilnil // Absence means completion has not been reserved.
func readExecutionCompletionSeal(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) (*ExecutionCompletionSeal, error) {
	repository := query.ExecutionEvent

	event, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.EventType.Eq(string(ExecutionEventTypeCompletionSealed)),
	).Order(repository.Sequence.Desc()).First()
	if err != nil {
		err = ctxerrors.Wrap(err, "read completion boundary history")
		if errors.Is(err, commerr.ErrNotFound) {
			return nil, nil
		}

		return nil, err
	}

	var seal ExecutionCompletionSeal
	if err := json.Unmarshal([]byte(event.PayloadJSON), &seal); err != nil {
		return nil, ctxerrors.Wrap(err, "decode completion boundary")
	}

	if seal.ActivityID == uuid.Nil || seal.InboxSequence < 0 ||
		seal.ControlRevision < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"stored completion boundary is invalid")
	}

	return &seal, nil
}

func validateRepeatedCompletionSeal(
	seal *ExecutionCompletionSeal,
	activity *models.ExecutionActivity,
) (*ExecutionCompletionSeal, error) {
	if seal.ActivityID != activity.ID ||
		subtle.ConstantTimeCompare([]byte(seal.InputHash),
			[]byte(completionSealInputHash(activity))) != 1 {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"completion boundary belongs to another activity input")
	}

	return seal, nil
}

func completionSealInputHash(activity *models.ExecutionActivity) string {
	digest := sha256.Sum256([]byte(activity.InputJSON))

	return hex.EncodeToString(digest[:])
}

func (s *Store) createExecutionCompletionSeal(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
) (*ExecutionCompletionSeal, error) {
	if execution.EventCount == math.MaxInt64 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution completion event sequence exhausted")
	}

	sequence, err := completionInboxWatermark(ctx, query, execution)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read completion inbox watermark")
	}

	seal := &ExecutionCompletionSeal{
		ActivityID: activity.ID, InputHash: completionSealInputHash(activity),
		InboxSequence: sequence, ControlRevision: execution.ControlRevision,
	}

	encoded, err := json.Marshal(seal)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode completion boundary")
	}

	if err := s.appendExecutionEvent(ctx, query, execution,
		ExecutionEventTypeCompletionSealed, encoded); err != nil {
		return nil, ctxerrors.Wrap(err, "record completion boundary")
	}

	return seal, nil
}

func completionInboxWatermark(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) (int64, error) {
	repository := query.ExecutionInboxMessage

	row, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
	).Order(repository.Sequence.Desc()).First()
	if err != nil {
		err = ctxerrors.Wrap(err, "read last completion inbox sequence")
		if errors.Is(err, commerr.ErrNotFound) {
			return 0, nil
		}

		return 0, err
	}

	return row.Sequence, nil
}
