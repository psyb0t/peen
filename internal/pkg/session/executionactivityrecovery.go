package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const (
	// uncertainStepFormat names one uncertain step and its activity ID.
	uncertainStepFormat    = "%s (activity %s)"
	uncertainStepSeparator = ", "
)

func (s *Store) markActivityUncertain(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
) error {
	repository := query.ExecutionActivity

	_, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(
		repository.State.Value(string(models.ActivityStateUncertain)),
		repository.FailureDetail.Value(interruptedActivityReason),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "record uncertain activity")
	}

	activity.State = models.ActivityStateUncertain
	activity.FailureDetail = interruptedActivityReason

	if execution.State != models.ExecutionStatePaused &&
		!isExecutionTerminal(execution.State) {
		stored := query.Execution

		_, err := stored.WithContext(ctx).Where(
			stored.SessionID.Eq(execution.SessionID),
			stored.ID.Eq(execution.ID),
		).UpdateSimple(
			stored.State.Value(string(models.ExecutionStateReconciling)),
			stored.UpdatedAt.Value(s.now()),
		)
		if err != nil {
			return ctxerrors.Wrap(err, "mark execution for reconciliation")
		}
	}

	return s.appendActivityEvent(ctx, query, execution,
		activity, ExecutionEventTypeStepUncertain)
}

// ResolveExecutionActivity records inspection of an unknown effect.
// Resolving a step never resumes a paused execution.
func (s *Store) ResolveExecutionActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
	input ResolveActivityInput,
) (*models.ExecutionActivity, error) {
	if err := s.validateActivityResolution(input); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode activity resolution")
	}

	var result *models.ExecutionActivity

	err = s.query.Transaction(func(tx *repositories.Query) error {
		var resolveErr error

		result, resolveErr = s.resolveActivityWithQuery(ctx, tx,
			sessionID, activityID, input, string(payload), nil)
		if resolveErr != nil {
			return ctxerrors.Wrap(resolveErr, "resolve activity checkpoint")
		}

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve activity transaction")
	}

	return result, nil
}

func (s *Store) resolveActivityWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID, activityID uuid.UUID,
	input ResolveActivityInput,
	payload string,
	origin *executionControlOrigin,
) (*models.ExecutionActivity, error) {
	activity, err := s.findExecutionActivity(ctx, query, sessionID, activityID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find activity for resolution")
	}

	isDuplicate, err := activityResolutionStatus(activity, input,
		payload)
	if err != nil {
		return nil, s.pointToUncertainActivities(ctx, query, activity, err)
	}

	if isDuplicate {
		return activity, nil
	}

	execution, err := s.findExecution(
		ctx, query, sessionID, activity.ExecutionID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find activity resolution execution")
	}

	if err := s.applyActivityResolution(ctx, query, activity,
		input, payload); err != nil {
		return nil, ctxerrors.Wrap(err, "apply activity resolution")
	}

	if err := s.appendActivityResolution(ctx, query, execution,
		activity, input, origin); err != nil {
		return nil, err
	}

	if err := s.restoreReconciledExecution(ctx, query, execution); err != nil {
		return nil, err
	}

	return activity, nil
}

func activityResolutionStatus(
	activity *models.ExecutionActivity,
	input ResolveActivityInput,
	payload string,
) (bool, error) {
	if activity.Attempt != input.Attempt {
		return false, ctxerrors.Wrap(commerr.ErrConflict,
			"activity resolution attempt was replaced")
	}

	if activity.ResolutionJSON == payload {
		return true, nil
	}

	if activity.State != models.ActivityStateUncertain {
		return false, ctxerrors.Wrapf(commerr.ErrConflict,
			"activity %s is %s, not uncertain", activity.StepID,
			activity.State)
	}

	return false, nil
}

// pointToUncertainActivities adds the steps of the same execution that are
// uncertain to a refused resolution, so a caller that picked the wrong step
// sees which one to resolve.
func (s *Store) pointToUncertainActivities(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	refusal error,
) error {
	repository := query.ExecutionActivity

	uncertain, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ExecutionID.Eq(activity.ExecutionID),
		repository.State.Eq(string(models.ActivityStateUncertain)),
	).Order(repository.CreatedAt).Find()
	if err != nil {
		return errors.Join(refusal,
			ctxerrors.Wrap(err, "list uncertain activities"))
	}

	if len(uncertain) == 0 {
		return ctxerrors.Wrap(refusal, "this execution has no uncertain step")
	}

	steps := make([]string, 0, len(uncertain))
	for _, candidate := range uncertain {
		steps = append(steps, fmt.Sprintf(uncertainStepFormat,
			candidate.StepID, candidate.ID))
	}

	return ctxerrors.Wrapf(refusal, "the uncertain steps are %s",
		strings.Join(steps, uncertainStepSeparator))
}

func (s *Store) validateActivityResolution(input ResolveActivityInput) error {
	if input.Attempt < 1 || input.Evidence == "" ||
		len(input.Evidence) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"activity resolution requires inspection evidence")
	}

	switch input.Resolution {
	case ActivityResolutionRetry:
		if len(input.Output) != 0 || len(input.Context) != 0 {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"retry takes no output or context, leave both out to run "+
					"the step again")
		}

		return nil
	case ActivityResolutionAccept:
		if !isExecutionContext(input.Output) {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"accept needs output, the step's output as a JSON object")
		}

		if !isExecutionContext(input.Context) {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"accept needs context, the step's saved context as a JSON object, {} when it is empty") //nolint:lll // One model-facing refusal.
		}

		return s.validateActivityResult(CompleteActivityInput{
			Output: input.Output, Context: input.Context,
		})
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed, "activity resolution")
}

func (s *Store) applyActivityResolution(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input ResolveActivityInput,
	payload string,
) error {
	repository := query.ExecutionActivity

	_, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(repository.ResolutionJSON.Value(payload))
	if err != nil {
		return ctxerrors.Wrap(err, "save activity inspection decision")
	}

	activity.ResolutionJSON = payload
	if input.Resolution == ActivityResolutionAccept {
		return s.saveActivityResult(ctx, query, activity,
			CompleteActivityInput{Output: input.Output, Context: input.Context})
	}

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(
		repository.State.Value(string(models.ActivityStateScheduled)),
		repository.OwnerGenerationID.Value(""),
		repository.FailureDetail.Value(""),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "allow inspected activity retry")
	}

	activity.State = models.ActivityStateScheduled
	activity.OwnerGenerationID = ""
	activity.FailureDetail = ""

	return nil
}

func (s *Store) appendActivityResolution(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	input ResolveActivityInput,
	origin *executionControlOrigin,
) error {
	payload, err := json.Marshal(struct {
		ActivityID uuid.UUID `json:"activityId"`
		ResolveActivityInput
		*executionControlOrigin
	}{activity.ID, input, origin})
	if err != nil {
		return ctxerrors.Wrap(err, "encode activity inspection event")
	}

	return s.appendExecutionEvent(ctx, query, execution,
		ExecutionEventTypeStepResolved, payload)
}

func (s *Store) restoreReconciledExecution(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	if execution.State != models.ExecutionStateReconciling {
		return nil
	}

	activities := query.ExecutionActivity

	remaining, err := activities.WithContext(ctx).Where(
		activities.ExecutionID.Eq(execution.ID),
		activities.SessionID.Eq(execution.SessionID),
		activities.State.Eq(string(models.ActivityStateUncertain)),
	).Limit(1).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "find remaining uncertain activity")
	}

	if len(remaining) > 0 {
		return nil
	}

	repository := query.Execution

	_, err = repository.WithContext(ctx).Where(
		repository.ID.Eq(execution.ID),
		repository.SessionID.Eq(execution.SessionID),
	).UpdateSimple(repository.State.Value(string(models.ExecutionStateRunning)))
	if err != nil {
		return ctxerrors.Wrap(err, "restore reconciled execution")
	}

	return nil
}
