package agent

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) workflowChildToolCall(
	ctx context.Context,
	sink *agentRunSink,
	scope *directoryScope,
	input workflowToolInput,
) (workflowToolResult, error) {
	if err := r.validateChildWorkflowTool(ctx, sink, scope,
		input); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"validate child workflow tool")
	}

	registered, err := scope.provideTools(ctx)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err, "provide child tools")
	}

	event := elelem.ToolCallEvent{
		CallID: input.Call.ID, Name: input.Call.Name,
		Arguments: input.Call.Arguments, Index: input.Index,
	}
	if err := sink.onToolCallStart(ctx, event); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"record child workflow tool start")
	}

	outcome, err := runWorkflowTool(ctx, registered, input, workflowToolOptions{
		Timeout: r.toolTimeout, MaxResultTokens: r.maxToolResultTokens,
		Counter: modelTokenCounter(scope.options.deps.model),
		Finish:  scope.finishTool,
	})
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "run child workflow tool")
	}

	if err := checkpointChildWorkflowTool(ctx, sink, scope,
		event, outcome); err != nil {
		return outcome, ctxerrors.Wrap(err, "project child tool result")
	}

	outcome.Context, err = rootWorkflowToolContext(input, scope)
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "save child tool context")
	}

	return outcome, nil
}

func (r *Runtime) validateChildWorkflowTool(
	ctx context.Context,
	sink *agentRunSink,
	scope *directoryScope,
	input workflowToolInput,
) error {
	if scope == nil || scope.options.agentRunID == nil ||
		sink == nil || sink.run == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child tool scope and sink")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Round.Execution.Context,
		&conversation); err != nil {
		return ctxerrors.Wrap(err, "decode child tool input")
	}

	if err := r.validateChildWorkflowInbox(ctx, input.Round,
		&conversation); err != nil {
		return ctxerrors.Wrap(err, "validate child tool actor")
	}

	if *scope.options.agentRunID != conversation.Child.AgentRunID ||
		sink.run.ID != conversation.Child.AgentRunID ||
		sink.sessionID != conversation.Child.SessionID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child tool scope belongs to another run")
	}

	return r.validateChildToolActivity(ctx, input)
}

func (r *Runtime) validateChildToolActivity(
	ctx context.Context,
	input workflowToolInput,
) error {
	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child tool activity lease")
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child tool activity storage")
	}

	activity, err := store.GetExecutionActivity(ctx,
		input.Round.Execution.SessionID, lease.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read saved child tool activity")
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return ctxerrors.Wrap(err, "encode child tool activity input")
	}

	if !sameChildToolActivity(activity, lease, input, encoded) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child tool claim or saved input differs")
	}

	return nil
}

func sameChildToolActivity(
	activity *models.ExecutionActivity,
	lease session.ActivityLease,
	input workflowToolInput,
	encoded json.RawMessage,
) bool {
	return activity.ExecutionID == input.Round.Execution.ExecutionID &&
		activity.Kind == models.ActivityKindTool &&
		activity.State == models.ActivityStateRunning &&
		activity.Attempt == lease.Attempt &&
		activity.OwnerGenerationID == lease.GenerationID.String() &&
		bytes.Equal(encoded, []byte(activity.InputJSON))
}

func checkpointChildWorkflowTool(
	ctx context.Context,
	sink *agentRunSink,
	scope *directoryScope,
	event elelem.ToolCallEvent,
	outcome workflowToolResult,
) error {
	event.Result = &outcome.Result
	current := scope.context()
	directory := scope.resultDirectory(event.CallID, "")

	if err := sink.persistToolResult(ctx, event, directory,
		current.prompt); err != nil {
		return ctxerrors.Wrap(err, "record child workflow tool result")
	}

	for _, injection := range outcome.Injections {
		if err := sink.onMessageInjection(ctx, injection); err != nil {
			return ctxerrors.Wrap(err, "record child tool injection")
		}
	}

	return nil
}
