package agent

import "github.com/psyb0t/ctxerrors"

func (r *Runtime) rootWorkflowConversation(
	prepared *preparedTurn,
	scope *directoryScope,
) (workflowConversation, error) {
	current := scope.context()

	state, err := snapshotWorkflowRuntime(current)
	if err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"snapshot initial root workflow state")
	}

	if state.Directory.ContextHash == "" {
		state.Directory.ContextHash = prepared.contextHash
		state.Directory.PromptHash = prepared.promptHash
	}

	conversation := workflowConversation{
		Limits: &workflowExecutionLimits{
			MaxRounds:          r.maxToolRounds,
			MaxConcurrentTools: r.maxConcurrentTools,
		},
		ModelReference:      prepared.modelReference,
		SystemPrompt:        current.prompt,
		Messages:            directoryHistory(prepared.prompt.Messages()),
		MaxContextTokens:    r.maxContextTokens,
		OutputReserveTokens: r.outputReserveTokens,
		ReasoningEffort:     prepared.reasoningEffort,
		Runtime:             state, Root: r.rootWorkflowBootstrap(prepared),
		Compaction: snapshotWorkflowCompaction(prepared.compactor),
		Tools:      snapshotWorkflowToolDefinitions(current.tools),
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return workflowConversation{}, ctxerrors.Wrap(err,
			"validate initial compaction context")
	}

	return conversation, nil
}
