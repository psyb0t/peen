package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

const workflowWaitIdentityFormat = "wait:%d:%d:%s"

type workflowPassiveWait struct {
	ID              uuid.UUID  `json:"id"`
	Deadline        time.Time  `json:"deadline"`
	Question        string     `json:"question,omitempty"`
	Answer          *askOutput `json:"answer,omitempty"`
	Parent          bool       `json:"parent,omitempty"`
	DeliveryFailure string     `json:"deliveryFailure,omitempty"`
}

type workflowPassiveWaitContextKey struct{}

func workflowPassiveWaitID(input workflowToolInput) uuid.UUID {
	return uuid.NewSHA1(input.Round.Execution.ExecutionID,
		fmt.Appendf(nil, workflowWaitIdentityFormat, input.Round.Round,
			input.Index, input.Call.ID))
}

func retainWorkflowPassiveWait(
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
	now time.Time,
) {
	if input.Phase != workflowToolPhasePre || state.Finished ||
		state.Outcome.Failure != "" || state.Event == nil ||
		input.Tool.Call.Name != toolNameWaitMessages {
		return
	}

	var arguments waitMessagesInput
	if err := decodeToolArguments(input.Tool.Call.Arguments,
		&arguments); err != nil {
		// Invalid arguments follow the ordinary handler error path.
		return
	}

	timeout, err := optionalWaitTimeout(arguments.Timeout)
	if err != nil {
		// Invalid durations follow the ordinary handler error path.
		return
	}

	state.Wait = &workflowPassiveWait{ID: workflowPassiveWaitID(input.Tool)}
	if timeout > 0 {
		state.Wait.Deadline = now.Add(timeout)
	}
}

func validWorkflowPassiveWait(input workflowToolPhaseInput) bool {
	wait := input.State.Wait
	if wait == nil {
		return input.Phase != workflowToolPhaseQuestion &&
			input.State.LastPhase != workflowToolPhaseQuestion
	}

	if input.Phase == workflowToolPhasePre ||
		wait.ID != workflowPassiveWaitID(input.Tool) {
		return false
	}

	if input.Tool.Call.Name == toolNameWaitMessages {
		return validWorkflowMessageWait(input)
	}

	return validWorkflowQuestionRoute(input) && validRootWorkflowQuestion(input)
}

func validWorkflowMessageWait(input workflowToolPhaseInput) bool {
	wait := input.State.Wait

	return wait.Question == "" && wait.Answer == nil && !wait.Parent &&
		wait.DeliveryFailure == "" &&
		input.Phase != workflowToolPhaseQuestion &&
		input.State.LastPhase != workflowToolPhaseQuestion
}

func validWorkflowQuestionRoute(input workflowToolPhaseInput) bool {
	wait := input.State.Wait
	if !wait.Parent {
		return wait.DeliveryFailure == "" &&
			input.Phase != workflowToolPhaseQuestion &&
			input.State.LastPhase != workflowToolPhaseQuestion
	}

	if input.Phase == workflowToolPhaseQuestion {
		return wait.Answer == nil && wait.DeliveryFailure == ""
	}

	if input.Phase == workflowToolPhaseHandler {
		return input.State.LastPhase == workflowToolPhaseQuestion &&
			wait.Answer == nil
	}

	return wait.DeliveryFailure == "" || wait.Answer == nil
}

func workflowToolPhaseReplayPolicy(
	input workflowToolPhaseInput,
) models.ActivityReplayPolicy {
	if input.Phase == workflowToolPhaseHandler &&
		(input.Tool.Call.Name == toolNameSearchExecutions ||
			input.Tool.Call.Name == toolNameReadExecution) {
		return models.ActivityReplayPolicyRetry
	}

	if (input.Phase == workflowToolPhaseHandler ||
		input.Phase == workflowToolPhaseQuestion) &&
		input.State.Wait != nil && validWorkflowPassiveWait(input) {
		return models.ActivityReplayPolicyRetry
	}

	return models.ActivityReplayPolicyReconcile
}

func workflowPassiveWaitContext(
	ctx context.Context,
	input workflowToolPhaseInput,
) context.Context {
	if input.Phase != workflowToolPhaseHandler || input.State.Wait == nil {
		return ctx
	}

	return context.WithValue(ctx, workflowPassiveWaitContextKey{},
		*input.State.Wait)
}

func workflowPassiveWaitDeadline(
	ctx context.Context,
	timeout time.Duration,
) time.Time {
	value := ctx.Value(workflowPassiveWaitContextKey{})
	if wait, ok := value.(workflowPassiveWait); ok {
		return wait.Deadline
	}

	if timeout <= 0 {
		return time.Time{}
	}

	return time.Now().UTC().Add(timeout)
}
