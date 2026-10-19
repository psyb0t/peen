package session

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) validateFinishInput(input *FinishExecutionInput) error {
	if input == nil {
		return nil
	}

	if !validExecutionFinishState(input.State) ||
		(input.State == models.ExecutionStateCompleted &&
			input.FailureDetail != "") ||
		(input.State == models.ExecutionStateFailed &&
			input.FailureDetail == "") ||
		len(input.FailureDetail) > s.maxStoredMessageBytes ||
		len(input.WorkingDirectory) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution terminal result")
	}

	if err := s.validatePendingFinishOutput(input); err != nil {
		return ctxerrors.Wrap(err, "validate final continuation output")
	}

	return s.validateExecutionFinishChild(input)
}

func validExecutionFinishState(state models.ExecutionState) bool {
	return state == models.ExecutionStateCompleted ||
		state == models.ExecutionStateFailed
}

func (s *Store) validatePendingFinishOutput(
	input *FinishExecutionInput,
) error {
	if len(input.PendingOutput) != 0 &&
		(input.State != models.ExecutionStateCompleted ||
			!isExecutionContext(input.PendingOutput) ||
			len(input.PendingOutput) > s.maxStoredMessageBytes) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution pending continuation output")
	}

	return nil
}

func validateFinishClaim(
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
) error {
	if err := validateFinishIdentity(activity, input); err != nil {
		return err
	}

	if input.Finish == nil {
		return nil
	}

	if err := validateChildFinishClaim(execution, input.Finish); err != nil {
		return err
	}

	if execution.State == models.ExecutionStatePaused ||
		execution.State == models.ExecutionStateReconciling ||
		isExecutionTerminal(execution.State) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"execution cannot finish in its current state")
	}

	return nil
}

func validateFinishIdentity(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) error {
	if input.Finish == nil {
		if activity.Name == ExecutionFinishActivityName {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"final activity requires an execution result")
		}

		return nil
	}

	if activity.Name != ExecutionFinishActivityName ||
		!validExecutionFinishStepID(activity.StepID) ||
		activity.Kind != models.ActivityKindPreparation {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"activity cannot finish an execution")
	}

	return nil
}

func validateFinishedRepeat(
	execution *models.Execution,
	input CompleteActivityInput,
) error {
	if input.Finish == nil {
		return nil
	}

	finish := input.Finish
	if execution.State != finish.State ||
		execution.FailureDetail != finish.FailureDetail ||
		execution.ContextJSON != string(input.Context) ||
		(finish.WorkingDirectory != "" &&
			execution.WorkingDirectory != finish.WorkingDirectory) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"completed execution result differs")
	}

	return nil
}

// The activity output and terminal execution state share one transaction.
// A crash before its workflow acknowledgement therefore reuses both.
func (s *Store) finishExecution(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	input CompleteActivityInput,
	events *[]EventInput,
) error {
	if input.Finish == nil {
		return nil
	}

	if err := requireSettledExecutionActivities(ctx, query,
		execution, input.Finish.State); err != nil {
		return ctxerrors.Wrap(err, "settle activities before finalization")
	}

	if err := requireSettledExecutionInbox(ctx, query,
		execution, input.Finish.State); err != nil {
		return ctxerrors.Wrap(err, "settle inbox before finalization")
	}

	finish := input.Finish

	directory := execution.WorkingDirectory
	if finish.WorkingDirectory != "" {
		directory = finish.WorkingDirectory
	}

	if err := s.finishExecutionTurn(ctx, query, execution, finish); err != nil {
		return ctxerrors.Wrap(err, "finish original root turn")
	}

	if err := s.finishExecutionChild(ctx, query,
		execution, finish); err != nil {
		return ctxerrors.Wrap(err, "finish child execution run")
	}

	contextJSON, err := compactColumn(ctx, query, string(input.Context))
	if err != nil {
		return ctxerrors.Wrap(err, "compact execution terminal context")
	}

	repository := query.Execution

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ID.Eq(execution.ID),
	).UpdateSimple(
		repository.State.Value(string(finish.State)),
		repository.ContextJSON.Value(contextJSON),
		repository.WorkingDirectory.Value(directory),
		repository.FailureDetail.Value(finish.FailureDetail),
		repository.CompletedAt.Value(s.now()), repository.PausedAt.Null(),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "save execution terminal state")
	}

	return s.finishExecutionEvent(ctx, query, execution, input, events)
}

func (s *Store) finishExecutionEvent(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	input CompleteActivityInput,
	events *[]EventInput,
) error {
	finish := input.Finish

	kind := ExecutionEventTypeCompleted
	if finish.State == models.ExecutionStateFailed {
		kind = ExecutionEventTypeFailed
	}

	if err := s.appendExecutionEvent(ctx, query, execution, kind,
		input.Output); err != nil {
		return ctxerrors.Wrap(err, "append final execution event")
	}

	if finish.Turn != nil {
		*events = append([]EventInput(nil), finish.Turn.Events...)
	}

	return nil
}

func requireSettledExecutionActivities(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	terminal models.ExecutionState,
) error {
	settled := []string{string(models.ActivityStateCompleted)}
	if terminal == models.ExecutionStateFailed {
		settled = append(settled, string(models.ActivityStateFailed))
	}

	repository := query.ExecutionActivity

	count, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.State.NotIn(settled...),
	).Count()
	if err != nil {
		return ctxerrors.Wrap(err, "count unsettled execution activities")
	}

	if count != 0 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"execution still has unsettled activities")
	}

	return nil
}
