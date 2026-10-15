package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

func (r *Runtime) prepareChildWorkflowRound(
	ctx context.Context,
	scope *directoryScope,
	input workflowRoundInput,
) (json.RawMessage, error) {
	if scope == nil || scope.options.agentRunID == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child workflow preparation scope")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode child preparation context")
	}

	if err := r.validateChildWorkflowInbox(ctx, input,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "validate child preparation identity")
	}

	if *scope.options.agentRunID != conversation.Child.AgentRunID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child preparation scope belongs to another run")
	}

	current := scope.context()
	conversation.SystemPrompt = current.prompt
	conversation.Tools = snapshotWorkflowToolDefinitions(current.tools)

	if err := r.prepareChildWorkflowInbox(ctx, input,
		&conversation, current.skills); err != nil {
		return nil, ctxerrors.Wrap(err, "prepare child inbox context")
	}

	state, err := snapshotChildWorkflowRuntime(current)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "snapshot child preparation scope")
	}

	conversation.Runtime = state

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode child preparation context")
	}

	scope.applied = current
	scope.applyContext(current)

	return encoded, nil
}
