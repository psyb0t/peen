package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

func (r *Runtime) restoreChildWorkflowScope(
	ctx context.Context,
	deps *launchAgentDeps,
	run *AgentRun,
	conversation workflowConversation,
) (*launchAgentDeps, *preparedChildConversation, error) {
	if err := validateChildRestorationDependencies(deps, run,
		conversation); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "validate child restoration input")
	}

	stored, err := r.store.GetAgentRun(ctx, deps.sessionID, run.ID)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read restored child run")
	}

	if err := conversation.Child.validateAgainst(stored); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "validate restored child launch")
	}

	restoredDeps, err := r.restoreChildWorkflowDependencies(ctx, deps,
		conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "rebuild child dependencies")
	}

	restored, err := r.rebuildChildWorkflowScope(ctx, restoredDeps, run,
		conversation, stored)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore child scope state")
	}

	return restoredDeps, restored, nil
}

func (r *Runtime) rebuildChildWorkflowScope(
	ctx context.Context,
	deps *launchAgentDeps,
	run *AgentRun,
	conversation workflowConversation,
	stored *models.AgentRun,
) (*preparedChildConversation, error) {
	skills, err := restoreWorkflowSkills(conversation.Runtime.Skills)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore child skill deliveries")
	}

	saved := conversation.Child

	scope, err := newDirectoryScope(ctx, directoryScopeOptions{
		runtime: r, deps: deps,
		childInstructions: saved.Instructions, allowedTools: saved.AllowedTools,
		depth: int(saved.Depth), agentRunID: &run.ID,
		initialPrompt: conversation.SystemPrompt, skills: skills,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "rebuild child directory scope")
	}

	current := scope.context()
	if err := current.hooks.restoreWorkflowState(
		conversation.Runtime.Hooks); err != nil {
		return nil, ctxerrors.Wrap(err, "restore child hook state")
	}

	current.contextHash = conversation.Runtime.Directory.ContextHash
	current.promptHash = conversation.Runtime.Directory.PromptHash
	scope.applyContext(current)
	scope.completedRounds, scope.roundOffset = saved.CompletedRounds,
		saved.CompletedRounds
	scope.maximumRounds = saved.MaxRounds

	restored := &preparedChildConversation{
		scope: scope, maximumRounds: saved.MaxRounds,
		remainingRounds: saved.MaxRounds - saved.CompletedRounds,
		prompt: elelem.NewPrompt().WithSystem(conversation.SystemPrompt).
			WithHistory(conversation.Messages),
	}
	if conversation.Compaction != nil {
		restored.compactor, err = r.restoreChildWorkflowCompactor(stored,
			conversation.Compaction)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "restore child compaction state")
		}

		restored.compactor.options.Workspace = deps.executor.Workspace()
		restored.compactor.options.Hook = composeCompactionHooks(
			restored.compactor.options.Hook, scope.childCompactionHook(run))
		scope.bindChildCompactor(restored.compactor)
	}

	return restored, nil
}

func validateChildRestorationDependencies(
	deps *launchAgentDeps,
	run *AgentRun,
	conversation workflowConversation,
) error {
	if !hasChildRestorationState(deps, run, conversation) {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child restoration requires saved runtime state")
	}

	if err := conversation.Child.validate(); err != nil {
		return ctxerrors.Wrap(err, "validate child restoration bootstrap")
	}

	if conversation.Child.AgentRunID != run.ID ||
		conversation.Child.SessionID != deps.sessionID ||
		conversation.Child.TurnID != deps.parentTurnID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child restoration belongs to another launch")
	}

	return validateInitialChildConversation(conversation,
		conversation.Child.MaxRounds-conversation.Child.CompletedRounds)
}

func hasChildRestorationState(
	deps *launchAgentDeps,
	run *AgentRun,
	conversation workflowConversation,
) bool {
	return deps != nil && run != nil &&
		conversation.Runtime != nil && conversation.Child != nil &&
		conversation.SystemPrompt != ""
}

func (r *Runtime) restoreChildWorkflowDependencies(
	ctx context.Context,
	deps *launchAgentDeps,
	conversation workflowConversation,
) (*launchAgentDeps, error) {
	state := conversation.Runtime

	snapshot, err := harness.RestoreCheckpoint(state.Harness)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore frozen child harness")
	}

	if snapshot.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child harness and saved directory differ")
	}

	storedSession, err := r.store.Get(ctx, deps.sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read restored child session")
	}

	executor, err := r.newTurnExecutor(turnBasis{
		storedSession: storedSession, workspace: state.Directory.Directory,
	}, deps.sessionID, deps.parentTurnID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore child working directory")
	}

	if executor.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"saved child directory is not canonical")
	}

	if err := executor.RestoreObservations(state.Observations); err != nil {
		return nil, ctxerrors.Wrap(err, "restore child read observations")
	}

	model, err := r.models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve restored child connection")
	}

	restored := *deps
	restored.runtime = r
	restored.executor, restored.snapshot = executor, snapshot
	restored.model = retainedWorkflowModel(conversation, model)
	restored.modelReference = conversation.ModelReference
	restored.directoryScope = nil
	restored.workerGenerationID = workerGenerationReference(
		r.workerGenerationID)
	restored.userMessages = nil

	return &restored, nil
}
