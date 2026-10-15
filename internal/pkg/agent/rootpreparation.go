package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
)

func (r *Runtime) prepareRootWorkflowRound(
	ctx context.Context,
	prepared *preparedTurn,
	scope *directoryScope,
	input workflowRoundInput,
) (json.RawMessage, error) {
	if err := r.validateRootWorkflowModelInput(ctx,
		prepared, input); err != nil {
		return nil, err
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode root preparation context")
	}

	current := scope.context()
	if current.contextHash == "" {
		current.contextHash, current.promptHash = prepared.contextHash,
			prepared.promptHash
	}

	scope.applied = current
	scope.applyContext(current)
	conversation.SystemPrompt = current.prompt
	conversation.Tools = nil

	for _, tool := range current.tools.Definitions() {
		conversation.Tools = append(conversation.Tools, workflowToolDefinition{
			Name: tool.Name, Description: tool.Description,
			ArgumentsSchema: tool.ArgumentsSchema,
			StrictArguments: tool.StrictArguments,
		})
	}

	if err := r.prepareRootWorkflowInbox(ctx, input.Execution,
		&conversation, current.skills); err != nil {
		return nil, ctxerrors.Wrap(err, "prepare root inbox context")
	}

	state, err := snapshotWorkflowRuntime(current)
	if err != nil {
		return nil, err
	}

	conversation.Runtime = state
	if conversation.Root == nil {
		conversation.Root = r.rootWorkflowBootstrap(prepared)
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode root preparation context")
	}

	return encoded, nil
}
