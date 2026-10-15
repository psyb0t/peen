package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) prepareChildWorkflowFinish(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishResult, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"decode child final context")
	}

	if conversation.Runtime == nil {
		return workflowFinishResult{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "child final runtime context")
	}

	if err := r.validateChildWorkflowInbox(ctx, workflowRoundInput{
		Execution: input.Execution,
	}, &conversation); err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"validate child final actor")
	}

	messages, err := json.Marshal(conversation.Messages)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"encode final child conversation")
	}

	result := input.Result
	state := models.AgentRunStateCompleted
	classification := ""

	if result.Finish.State == models.ExecutionStateFailed {
		state, classification = models.AgentRunStateFailed, failureClassAgentRun
	}

	result.Finish.Child = &session.ExecutionFinishChild{
		State: state, ResponseText: result.Result.Text,
		ResponseThinking:      conversation.Reasoning,
		ResponseMessagesJSON:  string(messages),
		FinishReason:          string(conversation.Usage.FinishReason),
		PromptTokenCount:      conversation.Usage.Prompt,
		CompletionTokenCount:  conversation.Usage.Completion,
		FailureClassification: classification,
		FailureDetail:         result.Finish.FailureDetail,
	}
	result.Finish.WorkingDirectory = conversation.Runtime.Directory.Directory

	result.Finish.ChildReport, err = childWorkflowCompletionReport(
		conversation.Child, result.Finish.Child)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"frame child completion report")
	}

	return result, nil
}

func childWorkflowCompletionReport(
	child *workflowChildBootstrap,
	outcome *session.ExecutionFinishChild,
) (string, error) {
	result := AgentRunResult{
		State: string(outcome.State), Response: outcome.ResponseText,
		FailureDetail: outcome.FailureDetail,
	}

	text, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent, Name: child.Name,
		Run: child.AgentRunID.String(), Kind: agentResultKind(result.State),
		Text: agentResultText(result),
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "render child outcome message")
	}

	return text, nil
}
