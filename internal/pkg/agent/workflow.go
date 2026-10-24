package agent

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/registry"
	workflowworker "github.com/psyb0t/go-workflows/worker"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (e *workflowExecution) register(worker *workflowworker.Worker) error {
	if err := e.options.validate(); err != nil {
		return ctxerrors.Wrap(err, "validate workflow dependencies")
	}

	if err := worker.RegisterWorkflow(e.run,
		registry.WithName(agentWorkflowName)); err != nil {
		return ctxerrors.Wrap(err, "register agent execution workflow")
	}

	activities := []struct {
		name    string
		handler any
	}{
		{openingHookActivityName, e.openingHook},
		{modelActivityName, e.modelRound},
		{prepareActivityName, e.prepareRound},
		{toolActivityName, e.toolCall},
		{compactionActivityName, e.compactRound},
		{terminalHooksActivityName, e.terminalHooks},
		{toolPhaseActivityName, e.toolPhase},
		{session.ExecutionFinishActivityName, e.finishExecution},
	}
	for _, activity := range activities {
		if err := worker.RegisterActivity(activity.handler,
			registry.WithName(activity.name)); err != nil {
			return ctxerrors.Wrapf(err,
				"register workflow activity %s", activity.name)
		}
	}

	return nil
}

func workflowToolRoundFailure(results []workflowToolResult) error {
	for _, result := range results {
		if result.Failure != "" {
			return ctxerrors.New(result.Failure)
		}
	}

	return nil
}

func validWorkflowExecutionInput(input workflowExecutionInput) bool {
	return input.MaxRounds > 0 && input.MaxConcurrentTools > 0 &&
		input.SessionID != uuid.Nil && input.ExecutionID != uuid.Nil &&
		json.Valid(input.Context)
}

//nolint:ireturn // T is the concrete registered activity result.
func awaitWorkflowActivity[T any](
	ctx workflow.Context,
	name string,
	input any,
) (T, error) {
	result, err := workflow.ExecuteActivity[workflowActivityResult[T]](ctx,
		workflow.DefaultActivityOptions, name, input).Get(ctx)
	if err != nil {
		if !activityAttemptCancelled(ctx, err) {
			return *new(T), ctxerrors.Wrap(err, "execute workflow activity")
		}

		// go-workflows does not retry a cancelled attempt. The workflow
		// still runs, so the saved decision says whether the execution was
		// paused and must wait for resume, or can continue.
		result = workflowActivityResult[T]{
			Decision: session.ActivityDecisionReconcile,
		}
	}

	result, err = awaitWorkflowActivityResult(ctx, name, input, result)
	if err != nil {
		return *new(T), err
	}

	return result.Output, nil
}

// activityAttemptCancelled reports an activity attempt that ended because its
// own context was cancelled, as when its execution is paused, while the
// workflow itself is still running.
func activityAttemptCancelled(ctx workflow.Context, err error) bool {
	return errors.Is(err, workflow.Canceled) && ctx.Err() == nil
}

func awaitWorkflowActivityResult[T any](
	ctx workflow.Context,
	name string,
	input any,
	result workflowActivityResult[T],
) (workflowActivityResult[T], error) {
	for result.Decision == session.ActivityDecisionPaused ||
		result.Decision == session.ActivityDecisionReconcile {
		var err error

		// Another step may have consumed the signal for this execution.
		// Recheck its durable decision before waiting for another signal.
		result, err = workflow.ExecuteActivity[workflowActivityResult[T]](ctx,
			workflow.DefaultActivityOptions, name, input).Get(ctx)
		if err != nil {
			return result, ctxerrors.Wrap(err, "continue workflow activity")
		}

		if result.Decision != session.ActivityDecisionPaused &&
			result.Decision != session.ActivityDecisionReconcile {
			return result, nil
		}

		_, ok := workflow.NewSignalChannel[struct{}](ctx,
			workflowResumeSignal).Receive(ctx)
		if !ok {
			return result, ctxerrors.Wrap(workflow.Canceled,
				"workflow continuation wait cancelled")
		}
	}

	return result, nil
}
