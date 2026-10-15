package agent

import "github.com/psyb0t/ctxerrors"

func (r *Runtime) restoreWorkflowCompactor(
	prepared *preparedTurn,
	state *workflowCompactionState,
) (*compactor, error) {
	options := r.compactionOptions
	options.Store = r.store
	options.Models = r.models
	options.Metrics = r.metrics
	options.Workspace = prepared.workspace
	options.ModelReference = state.ModelReference
	options.Prompt = state.Prompt
	options.PromptHash = state.PromptHash
	options.MaxOutputTokens = state.MaxOutputTokens
	options.Timeout = state.Timeout
	options.Hook = composeCompactionHooks(options.Hook,
		prepared.runCompactionHook)

	sessionID := prepared.lease.SessionID

	built, err := newCompactor(options, sessionID, prepared.lease.TurnID,
		cloneCompactionPlan(state.Plan),
		sessionCompactionSink{store: r.store, sessionID: sessionID},
		cloneCompactionHead(state.Head))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "construct retained root compactor")
	}

	built.onCompacted = prepared.skillLedger.forgetThrough

	return built, nil
}
