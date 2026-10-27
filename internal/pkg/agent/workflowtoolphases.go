package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	toolPhaseActivityName = "toolPhaseV1"
)

type workflowToolPhase string

const (
	workflowToolPhasePre      workflowToolPhase = "preHook"
	workflowToolPhaseQuestion workflowToolPhase = "questionDelivery"
	workflowToolPhaseHandler  workflowToolPhase = "handler"
	workflowToolPhaseOutcome  workflowToolPhase = "outcomeHook"
	workflowToolPhasePost     workflowToolPhase = "postHook"
	workflowToolPhaseProject  workflowToolPhase = "project"
)

type workflowToolPhaseInput struct {
	Tool  workflowToolInput      `json:"tool"`
	Phase workflowToolPhase      `json:"phase"`
	State workflowToolPhaseState `json:"state"`
}

type workflowToolPhaseState struct {
	LastPhase     workflowToolPhase         `json:"lastPhase,omitempty"`
	Outcome       workflowToolResult        `json:"outcome"`
	Finished      bool                      `json:"finished,omitempty"`
	Pending       *workflowPendingDirectory `json:"pendingDirectory,omitempty"`
	Event         *workflowToolEventState   `json:"event,omitempty"`
	Deadline      time.Time                 `json:"deadline"`
	StreamAttempt int64                     `json:"streamAttempt,omitempty"`
	StartedAt     time.Time                 `json:"startedAt"`
	Wait          *workflowPassiveWait      `json:"wait,omitempty"`

	RootProjection  *rootToolProjection `json:"rootProjection,omitempty"`
	ChildProjection *childPhaseBuffer   `json:"childProjection,omitempty"`
}

func (e *workflowExecution) toolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
) (workflowActivityResult[workflowToolPhaseState], error) {
	if err := input.validate(); err != nil {
		return workflowActivityResult[workflowToolPhaseState]{}, err
	}

	if e.options.ToolPhase == nil {
		return workflowActivityResult[workflowToolPhaseState]{},
			ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
				"tool phase callback")
	}

	result, err := runWorkflowCheckpoint(ctx, e, input.Tool.Round.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("tool:%d:%d:%s", input.Tool.Round.Round,
				input.Tool.Index, input.Phase),
			Kind: models.ActivityKindTool, Name: toolPhaseActivityName,
			ReplayPolicy: workflowToolPhaseReplayPolicy(input),
		}, input, func(ctx context.Context) (workflowToolPhaseState, error) {
			output, err := e.options.ToolPhase(ctx, input)
			if err != nil {
				return output, ctxerrors.Wrap(err,
					"execute tool phase callback")
			}

			output.LastPhase = input.Phase
			output.Finished = output.Finished || output.Outcome.Failure != ""

			if bytes.Equal(output.Outcome.Context,
				input.Tool.Round.Execution.Context) {
				output.Outcome.Context = nil
			}

			return output, nil
		}, func(output workflowToolPhaseState) json.RawMessage {
			return workflowToolPhaseTurnContext(input, output)
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "checkpoint tool phase")
	}

	return e.deliverToolPhase(ctx, input, result)
}

// workflowToolPhaseTurnContext returns the turn context after a phase. A
// phase output carries a context only when the phase changed it, so each
// activity payload holds the context once.
func workflowToolPhaseTurnContext(
	input workflowToolPhaseInput,
	output workflowToolPhaseState,
) json.RawMessage {
	if len(output.Outcome.Context) != 0 {
		return output.Outcome.Context
	}

	return input.Tool.Round.Execution.Context
}

func (input workflowToolPhaseInput) validate() error {
	tool := input.Tool
	if !validWorkflowExecutionInput(tool.Round.Execution) ||
		tool.Round.Round < 0 ||
		tool.Round.Round >= tool.Round.Execution.MaxRounds || tool.Index < 0 ||
		strings.TrimSpace(tool.Call.ID) == "" ||
		strings.TrimSpace(tool.Call.Name) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool phase identity")
	}

	previous := input.State.LastPhase
	if !validWorkflowToolPhaseTransition(previous, input.Phase,
		input.State.Finished) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool phase predecessor")
	}

	if !validWorkflowPassiveWait(input) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool phase passive wait identity")
	}

	return nil
}

func validWorkflowToolPhaseTransition(
	previous, next workflowToolPhase,
	finished bool,
) bool {
	if finished {
		return next == workflowToolPhaseProject &&
			slices.Contains([]workflowToolPhase{
				workflowToolPhasePre, workflowToolPhaseQuestion,
				workflowToolPhaseHandler,
				workflowToolPhaseOutcome, workflowToolPhasePost,
			}, previous)
	}

	switch next {
	case workflowToolPhasePre:
		return previous == ""
	case workflowToolPhaseQuestion:
		return previous == workflowToolPhasePre
	case workflowToolPhaseHandler:
		return previous == workflowToolPhasePre ||
			previous == workflowToolPhaseQuestion
	case workflowToolPhaseOutcome:
		return previous == workflowToolPhaseHandler
	case workflowToolPhasePost:
		return previous == workflowToolPhaseOutcome
	case workflowToolPhaseProject:
		return previous == workflowToolPhasePost
	}

	return false
}
