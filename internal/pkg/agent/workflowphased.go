package agent

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/go-workflows/workflow"
)

type workflowToolPhaseCompletion struct {
	result workflowToolResult
	err    error
}

func (e *workflowExecution) run(
	ctx workflow.Context,
	input workflowExecutionInput,
) (workflowRoundResult, error) {
	if !validWorkflowExecutionInput(input) {
		return workflowRoundResult{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "workflow execution input")
	}

	ctx = withResumeGate(ctx)

	opened, err := openWorkflow(ctx, input)
	if err != nil {
		return finishFailedWorkflow(ctx, opened,
			workflowRoundResult{Context: opened.Context}, err)
	}

	input = opened

	result := workflowRoundResult{Context: input.Context}
	for round := input.NextRound; round < input.MaxRounds; round++ {
		var err error

		result, err = e.runPhasedRound(ctx, input, round)
		if err != nil {
			return finishFailedWorkflow(ctx, input, result, err)
		}

		if len(result.Calls) == 0 && !result.Continue {
			saved, err := terminalWorkflowFinish(ctx, input, result, round)
			if err != nil {
				return result, ctxerrors.Wrap(err,
					"finish phased tool workflow")
			}

			if !saved.Continue {
				return saved.Result, nil
			}

			result = saved.Result
		}

		input.Context = result.Context
		if workflowHistoryRolloverDue(input, round+1,
			workflow.InstanceExecutionDetails(ctx).HistoryLength) {
			input.NextRound = round + 1

			//nolint:wrapcheck // The SDK consumes this control error.
			return workflowRoundResult{}, workflow.ContinueAsNew(ctx, input)
		}
	}

	return finishFailedWorkflow(ctx, input, result, ctxerrors.Wrap(
		workflowRoundLimitError(input), "workflow round budget exhausted"))
}

func workflowHistoryRolloverDue(input workflowExecutionInput,
	nextRound int, historyLength int64,
) bool {
	return nextRound < input.MaxRounds && input.HistoryThreshold > 0 &&
		historyLength >= input.HistoryThreshold
}

func (e *workflowExecution) runPhasedRound(
	ctx workflow.Context,
	input workflowExecutionInput,
	round int,
) (workflowRoundResult, error) {
	request := workflowRoundInput{Execution: input, Round: round}

	if err := checkWorkflowTurnTokens(input); err != nil {
		return workflowRoundResult{Context: input.Context},
			ctxerrors.Wrap(err, "phased workflow token limit")
	}

	result, err := compactingWorkflowModelRound(ctx, request)
	if err != nil {
		return result, ctxerrors.Wrap(err, "phased workflow model round")
	}

	if len(result.Calls) == 0 && !result.Continue {
		return result, nil
	}

	if round == input.MaxRounds-1 {
		return result, ctxerrors.Wrap(workflowRoundLimitError(input),
			"phased workflow has no continuation round")
	}

	request.Execution.Context = result.Context

	calls, results, err := runPhasedWorkflowTools(ctx, request, result.Calls)
	if err != nil {
		return workflowRoundResult{Context: result.Context},
			ctxerrors.Wrap(err, "phased workflow tools")
	}

	result.Context, err = e.options.MergeRound(result.Context, calls, results)
	if err != nil {
		return result, ctxerrors.Wrap(err, "merge phased workflow round")
	}

	result.Continue = true

	return result, workflowToolRoundFailure(results)
}

func runPhasedWorkflowTool(
	ctx workflow.Context,
	tool workflowToolInput,
) (workflowToolResult, error) {
	tool.Compact = compactWorkflowTool(tool)

	state, err := initialWorkflowToolState(tool)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err, "restore tool start")
	}

	for _, phase := range []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseQuestion,
		workflowToolPhaseHandler,
		workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	} {
		if skipWorkflowToolPhase(tool, state, phase) {
			continue
		}

		var err error

		state, err = awaitWorkflowActivity[workflowToolPhaseState](ctx,
			toolPhaseActivityName, workflowToolPhaseInput{
				Tool: tool, Phase: phase, State: state,
			})
		if err != nil {
			return workflowToolResult{}, ctxerrors.Wrap(err,
				"continue phased workflow tool")
		}

		advanceWorkflowToolPhase(&tool, &state)
	}

	state.Outcome.Context = tool.Round.Execution.Context

	return state.Outcome, nil
}

func skipWorkflowToolPhase(
	tool workflowToolInput,
	state workflowToolPhaseState,
	phase workflowToolPhase,
) bool {
	if tool.Compact && phase != workflowToolPhaseHandler &&
		phase != workflowToolPhaseProject {
		return true
	}

	if phase == workflowToolPhaseQuestion &&
		(state.Wait == nil || !state.Wait.Parent) {
		return true
	}

	return state.Finished && phase != workflowToolPhaseProject
}

// advanceWorkflowToolPhase moves a context a phase changed into the tool
// input. The next phase reads the current context from there, so the state
// it receives does not carry a second copy.
func advanceWorkflowToolPhase(
	tool *workflowToolInput,
	state *workflowToolPhaseState,
) {
	if len(state.Outcome.Context) == 0 {
		return
	}

	tool.Round.Execution.Context = state.Outcome.Context
	state.Outcome.Context = nil
}

func runPhasedWorkflowTools(
	ctx workflow.Context,
	round workflowRoundInput,
	calls []elelem.ToolCall,
) ([]workflowToolInput, []workflowToolResult, error) {
	inputs := make([]workflowToolInput, len(calls))

	results := make([]workflowToolResult, len(calls))
	for start := 0; start < len(calls); {
		end := workflowRepeatBatchEnd(round, calls, start)

		channels := make(
			[]workflow.Channel[workflowToolPhaseCompletion], end-start,
		)
		for index := start; index < end; index++ {
			inputs[index] = workflowToolInput{
				Round: round, Call: calls[index], Index: index,
			}
			channels[index-start] = schedulePhasedWorkflowTool(ctx,
				inputs[index])
		}

		var batchError error

		for index, channel := range channels {
			completion, ok := channel.Receive(ctx)
			if !ok {
				return nil, nil, ctxerrors.Wrap(workflow.Canceled,
					"phased tool wait cancelled")
			}

			if completion.err != nil {
				batchError = errors.Join(batchError, completion.err)

				continue
			}

			results[start+index] = completion.result
		}

		if batchError != nil {
			return nil, nil, ctxerrors.Wrap(batchError,
				"settle phased workflow tool batch")
		}

		updated, err := advanceWorkflowRepeatRound(round, results[start:end])
		if err != nil {
			return nil, nil, ctxerrors.Wrap(err, "retain batch repeat counts")
		}

		round = updated
		start = end
	}

	return inputs, results, nil
}

func schedulePhasedWorkflowTool(
	ctx workflow.Context,
	input workflowToolInput,
) workflow.Channel[workflowToolPhaseCompletion] {
	channel := workflow.NewBufferedChannel[workflowToolPhaseCompletion](1)

	workflow.Go(ctx, func(ctx workflow.Context) {
		result, err := runPhasedWorkflowTool(ctx, input)
		channel.Send(ctx, workflowToolPhaseCompletion{result: result, err: err})
	})

	return channel
}
