package session

import (
	"context"
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

const (
	maximumActivityIdentityBytes = 512
	interruptedActivityReason    = "previous activity outcome is unknown"
	emptyActivityJSON            = `{}`
)

// BeginExecutionActivity saves the input before external work begins.
// Completed results are reused; a replaced writer cannot overwrite a new claim.
func (s *Store) BeginExecutionActivity(
	ctx context.Context,
	sessionID, executionID, generationID uuid.UUID,
	input ScheduleActivityInput,
) (*ActivityClaim, error) {
	if generationID == uuid.Nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"activity worker generation")
	}

	if err := s.validateActivityInput(input); err != nil {
		return nil, err
	}

	var result *ActivityClaim

	err := s.query.Transaction(func(tx *repositories.Query) error {
		execution, err := s.findExecution(ctx, tx, sessionID, executionID)
		if err != nil {
			return ctxerrors.Wrap(err, "find activity execution")
		}

		activity, err := s.ensureExecutionActivity(ctx, tx, execution, input)
		if err != nil {
			return ctxerrors.Wrap(err, "save activity input")
		}

		result, err = s.claimExecutionActivity(ctx, tx, execution,
			activity, generationID)
		if err != nil {
			return ctxerrors.Wrap(err, "claim execution activity")
		}

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "begin activity transaction")
	}

	ctxscope.GetLogger(ctx).Info("execution activity claim recorded",
		"session_id", sessionID, "execution_id", executionID,
		"activity_id", result.Activity.ID, "decision", result.Decision,
		"attempt", result.Activity.Attempt)

	return result, nil
}

func (s *Store) validateActivityInput(input ScheduleActivityInput) error {
	if input.StepID == "" || input.Name == "" ||
		len(input.StepID) > maximumActivityIdentityBytes ||
		len(input.Name) > maximumActivityIdentityBytes ||
		!isExecutionContext(input.Input) ||
		len(input.Input) > s.maxExecutionStateBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "activity input")
	}

	return validateActivityPolicy(input)
}

func validateActivityPolicy(input ScheduleActivityInput) error {
	if input.ReplayPolicy != models.ActivityReplayPolicyRetry &&
		input.ReplayPolicy != models.ActivityReplayPolicyReconcile {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed, "activity replay policy",
		)
	}

	switch input.Kind {
	case models.ActivityKindModel, models.ActivityKindCompaction,
		models.ActivityKindPreparation:
		if input.ReplayPolicy != models.ActivityReplayPolicyRetry {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"activity kind requires retry policy")
		}

		return nil
	case models.ActivityKindTool:
		return nil
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed, "activity kind")
}

func (s *Store) ensureExecutionActivity(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	input ScheduleActivityInput,
) (*models.ExecutionActivity, error) {
	repository := query.ExecutionActivity

	activity, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.StepID.Eq(input.StepID),
	).First()
	if err != nil {
		err = ctxerrors.Wrap(err, "find scheduled activity")
	}

	if err == nil {
		if !sameActivityInput(activity, input) {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"saved activity input differs")
		}

		return activity, nil
	}

	if !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "find scheduled activity")
	}

	activity = &models.ExecutionActivity{
		ID: s.newID(), SessionID: execution.SessionID,
		ExecutionID: execution.ID,
		StepID:      input.StepID, Kind: input.Kind,
		State: models.ActivityStateScheduled, ReplayPolicy: input.ReplayPolicy,
		Name: input.Name, InputJSON: string(input.Input),
		ContextJSON:    emptyActivityJSON,
		ResolutionJSON: emptyActivityJSON, CreatedAt: s.now(),
	}

	activity.InputJSON, err = compactColumn(ctx, query, string(input.Input))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "compact activity input")
	}

	if err := repository.WithContext(ctx).Create(activity); err != nil {
		return nil, ctxerrors.Wrap(err, "schedule execution activity")
	}

	activity.InputJSON = string(input.Input)

	if err := s.appendActivityEvent(ctx, query, execution,
		activity, ExecutionEventTypeStepScheduled); err != nil {
		return nil, err
	}

	return activity, nil
}

func sameActivityInput(
	activity *models.ExecutionActivity,
	input ScheduleActivityInput,
) bool {
	return activity.Kind == input.Kind && activity.Name == input.Name &&
		activity.ReplayPolicy == input.ReplayPolicy &&
		activity.InputJSON == string(input.Input)
}

func (s *Store) claimExecutionActivity(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	generationID uuid.UUID,
) (*ActivityClaim, error) {
	result := &ActivityClaim{Activity: activity}
	if execution.State == models.ExecutionStatePaused {
		result.Decision = ActivityDecisionPaused

		return result, nil
	}

	if activity.State == models.ActivityStateCompleted {
		result.Decision = ActivityDecisionReuse

		return result, nil
	}

	if isExecutionTerminal(execution.State) {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution is terminal")
	}

	if activity.State == models.ActivityStateUncertain {
		result.Decision = ActivityDecisionReconcile

		return result, nil
	}

	if activity.State == models.ActivityStateRunning {
		if activity.OwnerGenerationID == generationID.String() {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"activity already has a live claim")
		}

		if activity.ReplayPolicy == models.ActivityReplayPolicyReconcile {
			if err := s.markActivityUncertain(ctx, query,
				execution, activity); err != nil {
				return nil, err
			}

			result.Decision = ActivityDecisionReconcile

			return result, nil
		}
	}

	if err := s.startActivityAttempt(ctx, query, execution,
		activity, generationID); err != nil {
		return nil, err
	}

	result.Decision = ActivityDecisionExecute
	result.Lease = ActivityLease{
		ID: activity.ID, GenerationID: generationID, Attempt: activity.Attempt,
	}

	return result, nil
}

func (s *Store) startActivityAttempt(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	generationID uuid.UUID,
) error {
	if err := s.claimExecutionChild(ctx, query, execution,
		generationID); err != nil {
		return ctxerrors.Wrap(err, "claim activity child")
	}

	if activity.Attempt == math.MaxInt64 {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState, "activity attempt overflow",
		)
	}

	now := s.now()
	repository := query.ExecutionActivity

	_, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(
		repository.State.Value(string(models.ActivityStateRunning)),
		repository.Attempt.Value(activity.Attempt+1),
		repository.OwnerGenerationID.Value(generationID.String()),
		repository.StartedAt.Value(now), repository.CompletedAt.Null(),
		repository.FailureDetail.Value(""),
		repository.ResolutionJSON.Value(emptyActivityJSON),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "start activity attempt")
	}

	activity.State = models.ActivityStateRunning
	activity.Attempt++
	activity.OwnerGenerationID = generationID.String()
	activity.StartedAt = &now
	activity.CompletedAt = nil
	activity.FailureDetail = ""
	activity.ResolutionJSON = emptyActivityJSON

	if execution.State == models.ExecutionStateQueued {
		stored := query.Execution

		_, err := stored.WithContext(ctx).Where(
			stored.SessionID.Eq(execution.SessionID),
			stored.ID.Eq(execution.ID),
		).UpdateSimple(stored.State.Value(string(models.ExecutionStateRunning)))
		if err != nil {
			return ctxerrors.Wrap(err, "mark execution running")
		}

		execution.State = models.ExecutionStateRunning
	}

	return s.appendActivityEvent(ctx, query, execution,
		activity, ExecutionEventTypeStepStarted)
}

func (s *Store) appendActivityEvent(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	kind ExecutionEventType,
) error {
	payload, err := json.Marshal(struct {
		ActivityID uuid.UUID           `json:"activityId"`
		StepID     string              `json:"stepId"`
		Kind       models.ActivityKind `json:"kind"`
		Attempt    int64               `json:"attempt"`
	}{activity.ID, activity.StepID, activity.Kind, activity.Attempt})
	if err != nil {
		return ctxerrors.Wrap(err, "encode activity event")
	}

	return s.appendExecutionEvent(ctx, query, execution, kind, payload)
}
