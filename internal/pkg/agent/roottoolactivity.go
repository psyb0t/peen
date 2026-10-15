package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey/elelemstream"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

type workflowRuntimeState struct {
	Directory    directoryMarker      `json:"directory"`
	Observations []tools.ObservedPath `json:"observations"`
	Harness      json.RawMessage      `json:"harness,omitempty"`
	Skills       *workflowSkillState  `json:"skills,omitempty"`
	Hooks        *workflowHookState   `json:"hooks,omitempty"`
}

func (r *Runtime) workflowRootToolCall(
	ctx context.Context,
	prepared *preparedTurn,
	scope *directoryScope,
	input workflowToolInput,
) (workflowToolResult, error) {
	if err := r.validateRootWorkflowModelInput(ctx,
		prepared, input.Round); err != nil {
		return workflowToolResult{}, err
	}

	adapter, err := prepared.workflowToolAdapter(ctx, input)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"restore root tool stream")
	}

	registered, err := scope.provideTools(ctx)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"provide root workflow tools")
	}

	event := elelem.ToolCallEvent{
		CallID: input.Call.ID, Name: input.Call.Name,
		Arguments: input.Call.Arguments, Index: input.Index,
	}
	if err := prepared.onToolCallStart(ctx, event); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"checkpoint root workflow tool start")
	}

	if err := adapter.OnToolCallStart(ctx, event); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"stream root workflow tool start")
	}

	outcome, err := runWorkflowTool(ctx, registered, input, workflowToolOptions{
		Timeout: r.toolTimeout, MaxResultTokens: r.maxToolResultTokens,
		Counter: modelTokenCounter(prepared.model), Finish: scope.finishTool,
	})
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "run root workflow tool")
	}

	if err := prepared.checkpointWorkflowTool(ctx, scope, adapter,
		event, outcome); err != nil {
		return outcome, ctxerrors.Wrap(err, "project root workflow tool result")
	}

	outcome.Context, err = rootWorkflowToolContext(input, scope)
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "save root workflow tool context")
	}

	return outcome, nil
}

func (p *preparedTurn) checkpointWorkflowTool(
	ctx context.Context,
	scope *directoryScope,
	adapter *elelemstream.Adapter,
	event elelem.ToolCallEvent,
	outcome workflowToolResult,
) error {
	event.Result = &outcome.Result
	directory := scope.resultDirectory(event.CallID, p.workspace)

	if err := p.recordToolResult(ctx, event, directory); err != nil {
		return ctxerrors.Wrap(err, "checkpoint root workflow tool result")
	}

	if err := adapter.OnToolResult(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "stream root workflow tool result")
	}

	for _, injection := range outcome.Injections {
		if err := p.onMessageInjection(ctx, injection); err != nil {
			return ctxerrors.Wrap(err, "checkpoint root tool injection")
		}
	}

	return nil
}

func rootWorkflowToolContext(
	input workflowToolInput,
	scope *directoryScope,
) (json.RawMessage, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Round.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode root tool context")
	}

	current := scope.context()

	state, err := snapshotWorkflowRuntime(current)
	if err != nil {
		return nil, err
	}

	conversation.Runtime = state
	conversation.SystemPrompt = current.prompt

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode root tool context")
	}

	return encoded, nil
}

func snapshotWorkflowRuntime(
	current *directoryContext,
) (*workflowRuntimeState, error) {
	checkpoint, err := current.snapshot.Checkpoint()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "checkpoint workflow harness")
	}

	return &workflowRuntimeState{
		Directory: directoryMarker{
			Directory:   current.executor.Workspace(),
			ContextHash: current.contextHash, PromptHash: current.promptHash,
		},
		Observations: current.executor.SnapshotObservations(),
		Harness:      checkpoint,
		Skills:       current.skills.workflowState(),
		Hooks:        current.hooks.workflowState(),
	}, nil
}
