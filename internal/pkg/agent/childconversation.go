package agent

import (
	"bytes"
	"context"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

func (r *Runtime) childWorkflowConversation(
	ctx context.Context,
	deps *launchAgentDeps,
	launch preparedChildLaunch,
	run *AgentRun,
	prepared *preparedChildConversation,
) (workflowConversation, error) {
	if !hasChildConversationDependencies(deps, run, prepared) {
		return workflowConversation{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "child conversation dependencies")
	}

	stored, err := r.store.GetAgentRun(ctx, deps.sessionID, run.ID)
	if err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err, "read child launch")
	}

	bootstrap, err := snapshotChildWorkflowBootstrap(stored,
		prepared.maximumRounds, launch.completedRounds)
	if err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"snapshot child launch")
	}

	if err := validatePreparedChildLaunch(bootstrap, launch,
		prepared); err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"validate prepared child launch")
	}

	model, streaming := deps.model.Model, true
	bootstrap.Model = &model
	bootstrap.AllowedTools = slices.Clone(launch.definition.allowedTools)
	current := prepared.scope.context()

	state, err := snapshotChildWorkflowRuntime(current)
	if err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"snapshot child runtime context")
	}

	conversation := workflowConversation{
		Limits: &workflowExecutionLimits{
			MaxRounds:          prepared.remainingRounds,
			MaxConcurrentTools: r.maxConcurrentTools,
		},
		ModelReference: deps.modelReference, SystemPrompt: current.prompt,
		Messages:         directoryHistory(prepared.prompt.Messages()),
		MaxContextTokens: r.maxContextTokens, Streaming: &streaming,
		Runtime: state, Child: bootstrap,
		Compaction: snapshotWorkflowCompaction(prepared.compactor),
		Tools:      snapshotWorkflowToolDefinitions(current.tools),
	}
	if err := validateInitialChildConversation(conversation,
		prepared.remainingRounds); err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"validate initial child context")
	}

	return conversation, nil
}

func snapshotChildWorkflowRuntime(
	current *directoryContext,
) (*workflowRuntimeState, error) {
	state, err := snapshotWorkflowRuntime(current)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "snapshot child runtime")
	}

	if state.Directory.ContextHash == "" {
		state.Directory.ContextHash = current.snapshot.Hash()
		state.Directory.PromptHash = hash(current.prompt)
	}

	return state, nil
}

func hasChildConversationDependencies(
	deps *launchAgentDeps,
	run *AgentRun,
	prepared *preparedChildConversation,
) bool {
	return deps != nil && run != nil && prepared != nil && prepared.scope != nil
}

func validateInitialChildConversation(
	conversation workflowConversation,
	remainingRounds int,
) error {
	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return ctxerrors.Wrap(err, "validate initial child compaction map")
	}

	return validateWorkflowChildConversation(conversation, workflowRoundInput{
		Execution: workflowExecutionInput{
			SessionID: conversation.Child.SessionID, MaxRounds: remainingRounds,
		},
	})
}

func validatePreparedChildLaunch(
	saved *workflowChildBootstrap,
	launch preparedChildLaunch,
	prepared *preparedChildConversation,
) error {
	if launch.definition.name != saved.Name ||
		launch.definition.instructions != saved.Instructions ||
		launch.definition.kind != string(saved.Definition) ||
		int64(launch.depth) != saved.Depth ||
		!slices.Equal(launch.definition.allowedTools, saved.AllowedTools) ||
		prepared.remainingRounds != saved.MaxRounds-saved.CompletedRounds {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"prepared child differs from accepted launch")
	}

	return nil
}

func snapshotWorkflowToolDefinitions(
	tools *elelem.ToolSet,
) []workflowToolDefinition {
	definitions := tools.Definitions()
	if len(definitions) == 0 {
		return nil
	}

	saved := make([]workflowToolDefinition, 0, len(definitions))
	for _, tool := range definitions {
		saved = append(saved, workflowToolDefinition{
			Name: tool.Name, Description: tool.Description,
			ArgumentsSchema: bytes.Clone(tool.ArgumentsSchema),
			StrictArguments: tool.StrictArguments,
		})
	}

	return saved
}
