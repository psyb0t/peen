package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

func (r *Runtime) restoreRootWorkflowScope(
	ctx context.Context,
	prepared *preparedTurn,
	conversation workflowConversation,
) (*preparedTurn, *directoryScope, error) {
	restored, err := r.restoreRootWorkflowState(ctx, prepared, conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore saved root state")
	}

	scope, err := r.rootDirectoryScope(ctx, restored)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore root directory scope")
	}

	current := scope.context()
	if err := current.hooks.restoreWorkflowState(
		conversation.Runtime.Hooks); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore root hook state")
	}

	current.contextHash = restored.contextHash
	current.promptHash = restored.promptHash
	scope.applyContext(current)

	return restored, scope, nil
}

func (r *Runtime) restoreRootWorkflowState(
	ctx context.Context,
	prepared *preparedTurn,
	conversation workflowConversation,
) (*preparedTurn, error) {
	if !hasRootRestorationState(prepared, conversation) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root workflow restoration requires saved runtime state")
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "validate restored compaction state")
	}

	restored := *prepared
	state := conversation.Runtime

	snapshot, err := harness.RestoreCheckpoint(state.Harness)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore root harness")
	}

	if snapshot.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"saved root harness and directory differ")
	}

	restored.executor, err = prepared.executor.AtCanonicalDirectory(ctx,
		state.Directory.Directory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore root executor")
	}

	if restored.executor.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"saved root directory is not canonical")
	}

	if err := restored.executor.RestoreObservations(
		state.Observations); err != nil {
		return nil, ctxerrors.Wrap(err, "restore root read observations")
	}

	restored.skillLedger, err = restoreWorkflowSkills(state.Skills)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore root skill deliveries")
	}

	restored.snapshot = snapshot
	if err := r.restoreRootWorkflowPrompt(&restored, conversation); err != nil {
		return nil, ctxerrors.Wrap(err,
			"restore saved root prompt and queue")
	}

	return &restored, nil
}

func (r *Runtime) restoreRootWorkflowPrompt(
	restored *preparedTurn,
	conversation workflowConversation,
) error {
	state := conversation.Runtime
	restored.workspace = restored.executor.Workspace()
	restored.contextHash = state.Directory.ContextHash
	restored.promptHash = state.Directory.PromptHash
	restored.prompt = elelem.NewPrompt().WithSystem(conversation.SystemPrompt).
		WithHistory(conversation.Messages)
	restored.toolHooks = nil

	if conversation.Compaction != nil {
		built, err := r.restoreWorkflowCompactor(restored,
			conversation.Compaction)
		if err != nil {
			return ctxerrors.Wrap(err, "restore saved root compactor")
		}

		restored.compactor = built
	} else if restored.compactor != nil {
		compaction := *restored.compactor
		restored.compactor = &compaction
	}

	if err := r.restoreRootWorkflowQueue(restored); err != nil {
		return ctxerrors.Wrap(err, "rebuild saved root queue")
	}

	return nil
}

func hasRootRestorationState(
	prepared *preparedTurn,
	conversation workflowConversation,
) bool {
	return prepared != nil && prepared.executor != nil &&
		conversation.Runtime != nil && conversation.SystemPrompt != ""
}
