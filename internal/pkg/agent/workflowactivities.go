package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	workflowCapacityEvidence      = "actor admission refused before effects"
	workflowRootAdmissionEvidence = "root turn admission refused before effects"
)

func (e *workflowExecution) modelRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[workflowRoundResult], error) {
	result, err := runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("model:%d", input.Round),
			Kind:   models.ActivityKindModel, Name: modelActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (workflowRoundResult, error) {
			return e.options.ModelRound(ctx, input)
		}, func(result workflowRoundResult) json.RawMessage {
			return result.Context
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "run model checkpoint")
	}

	return e.deliverModelRound(ctx, input, result)
}

func (e *workflowExecution) toolCall(
	ctx context.Context,
	input workflowToolInput,
) (workflowActivityResult[workflowToolResult], error) {
	return runWorkflowCheckpoint(ctx, e, input.Round.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("tool:%d:%d", input.Round.Round, input.Index),
			Kind:   models.ActivityKindTool, Name: toolActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
		}, input, func(ctx context.Context) (workflowToolResult, error) {
			return e.options.ToolCall(ctx, input)
		}, func(result workflowToolResult) json.RawMessage {
			if len(result.Context) != 0 {
				return result.Context
			}

			return input.Round.Execution.Context
		})
}

func runWorkflowCheckpoint[T any](
	ctx context.Context,
	e *workflowExecution,
	execution workflowExecutionInput,
	descriptor session.ScheduleActivityInput,
	input any,
	run func(context.Context) (T, error),
	contextAfter func(T) json.RawMessage,
) (workflowActivityResult[T], error) {
	ctx, finish := e.activityContext(ctx, execution)
	defer finish()

	encoded, err := json.Marshal(input)
	if err != nil {
		return workflowActivityResult[T]{}, ctxerrors.Wrap(err,
			"encode workflow activity input")
	}

	descriptor.Input = encoded

	claim, err := e.options.Store.BeginExecutionActivity(ctx,
		execution.SessionID, execution.ExecutionID,
		e.options.GenerationID, descriptor)
	if err != nil {
		return workflowActivityResult[T]{}, ctxerrors.Wrap(err,
			"claim workflow activity")
	}

	result := workflowActivityResult[T]{Decision: claim.Decision}
	if claim.Decision == session.ActivityDecisionReuse {
		if err := json.Unmarshal([]byte(*claim.Activity.OutputJSON),
			&result.Output); err != nil {
			return result, ctxerrors.Wrap(err, "decode saved activity output")
		}

		return result, nil
	}

	if claim.Decision != session.ActivityDecisionExecute {
		return result, nil
	}

	ctx = ctxscope.Set(ctx,
		ctxscope.Attr("activity_id", claim.Activity.ID.String()),
		ctxscope.Attr("activity_step", claim.Activity.StepID),
		ctxscope.Attr("activity_attempt", claim.Lease.Attempt),
	)
	ctx = context.WithValue(ctx, workflowActivityLeaseContextKey{}, claim.Lease)

	result.Output, err = invokeWorkflowPhase(ctx, run)
	if err != nil {
		return interruptedWorkflowCheckpoint[T](ctx, e, execution, claim, err)
	}

	result.Output, err = commitWorkflowPhase(ctx, e, execution, claim,
		result.Output, contextAfter(result.Output))
	if err != nil {
		return interruptedWorkflowCheckpoint[T](ctx, e, execution, claim, err)
	}

	return result, nil
}

//nolint:ireturn,nonamedreturns // Typed result and deferred panic recovery.
func invokeWorkflowPhase[T any](
	ctx context.Context,
	run func(context.Context) (T, error),
) (output T, err error) {
	defer func() {
		if recover() != nil {
			ctxscope.GetLogger(ctx).Error("workflow activity panicked",
				"reason", "activity_panic")

			err = ctxerrors.New("workflow activity panicked")
		}
	}()

	if err := ctx.Err(); err != nil {
		return output, ctxerrors.Wrap(err, "workflow phase cancelled")
	}

	return run(ctx)
}

//nolint:ireturn // T is the concrete phase result decoded from its checkpoint.
func commitWorkflowPhase[T any](
	ctx context.Context,
	e *workflowExecution,
	execution workflowExecutionInput,
	claim *session.ActivityClaim,
	proposed T,
	contextAfter json.RawMessage,
) (T, error) {
	encoded, err := saveWorkflowCheckpoint(ctx, e, execution, claim,
		proposed, contextAfter)
	if err != nil {
		return *new(T), ctxerrors.Wrap(err, "commit workflow phase")
	}

	var saved T
	if err := json.Unmarshal(encoded, &saved); err != nil {
		return saved, ctxerrors.Wrap(err, "decode committed workflow result")
	}

	return saved, nil
}

func saveWorkflowCheckpoint(
	ctx context.Context,
	e *workflowExecution,
	execution workflowExecutionInput,
	claim *session.ActivityClaim,
	result any,
	contextAfter json.RawMessage,
) (json.RawMessage, error) {
	output, err := json.Marshal(result)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode workflow activity output")
	}

	input := session.CompleteActivityInput{
		Output: output, Context: contextAfter,
	}
	input.RootProjection = rootPhaseTranscript(result)
	input.ChildProjection = childPhaseTranscript(result)

	if compacted, ok := result.(workflowCompactionResult); ok {
		input.Compaction = compacted.Compaction
	}

	if final, ok := result.(workflowFinishResult); ok {
		input.Finish = &final.Finish
		if final.AllowContinuation {
			final.Continue = true

			input.Finish.PendingOutput, err = json.Marshal(final)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "encode final continuation")
			}
		}
	}

	activity, err := e.options.Store.CompleteExecutionActivity(
		context.WithoutCancel(ctx), execution.SessionID, claim.Lease,
		input)
	if err != nil {
		return nil, ctxerrors.Wrap(err,
			"checkpoint workflow activity output")
	}

	if activity == nil || activity.OutputJSON == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"committed activity output")
	}

	return json.RawMessage(*activity.OutputJSON), nil
}

func interruptedWorkflowCheckpoint[T any](
	ctx context.Context,
	e *workflowExecution,
	execution workflowExecutionInput,
	claim *session.ActivityClaim,
	cause error,
) (workflowActivityResult[T], error) {
	isCapacityRefusal := errors.Is(cause, ErrTooManyAgentRuns)
	cause = e.pauseForCapacity(ctx, execution, cause)

	if err := e.options.Store.InterruptExecutionActivity(
		context.WithoutCancel(ctx), execution.SessionID, claim.Lease,
	); err != nil {
		return workflowActivityResult[T]{}, ctxerrors.Wrap(
			errors.Join(cause, err), "record workflow interruption")
	}

	state, err := e.options.Store.GetExecution(context.WithoutCancel(ctx),
		execution.SessionID, execution.ExecutionID)
	if err != nil {
		return workflowActivityResult[T]{}, ctxerrors.Wrap(err,
			"read interrupted workflow state")
	}

	isRootAdmissionRefusal := rootAdmissionRefused(state, cause)
	if isCapacityRefusal || isRootAdmissionRefusal {
		evidence := workflowCapacityEvidence
		if isRootAdmissionRefusal {
			evidence = workflowRootAdmissionEvidence
		}

		if err := e.retryUnstartedActivity(ctx, execution, claim,
			evidence); err != nil {
			return workflowActivityResult[T]{}, ctxerrors.Wrap(err,
				"retain unstarted admission refused activity")
		}
	}

	if state.State == models.ExecutionStatePaused || isRootAdmissionRefusal {
		return workflowActivityResult[T]{
			Decision: session.ActivityDecisionPaused,
		}, nil
	}

	if claim.Activity.ReplayPolicy == models.ActivityReplayPolicyReconcile {
		return workflowActivityResult[T]{
			Decision: session.ActivityDecisionReconcile,
		}, nil
	}

	return workflowActivityResult[T]{}, ctxerrors.Wrap(cause,
		"workflow activity interrupted")
}

func (e *workflowExecution) retryUnstartedActivity(
	ctx context.Context,
	execution workflowExecutionInput,
	claim *session.ActivityClaim,
	evidence string,
) error {
	if claim.Activity.ReplayPolicy != models.ActivityReplayPolicyReconcile {
		return nil
	}

	// Admission refused before any hook or handler could run.
	_, err := e.options.Store.ResolveExecutionActivity(
		context.WithoutCancel(ctx), execution.SessionID, claim.Activity.ID,
		session.ResolveActivityInput{
			Attempt:    claim.Lease.Attempt,
			Resolution: session.ActivityResolutionRetry,
			Evidence:   evidence,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "retain admission refusal evidence")
	}

	return nil
}
