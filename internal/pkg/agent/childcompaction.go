package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) workflowChildCompactRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	run, err := r.childCompactionExecution(ctx, input)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"validate child compaction execution")
	}

	if _, err := rootCompactionLease(ctx); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"validate child compaction activity lease")
	}

	units, err := selectWorkflowCompactionPrefix(r.models, input)
	if errors.Is(err, ErrCompactionUnavailable) {
		logNothingToSummarize(ctx, err)

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}

	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"select child compaction prefix")
	}

	if units == 0 {
		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"decode child compaction context")
	}

	built, err := r.restoreChildWorkflowCompactor(run, conversation.Compaction)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"restore child workflow compactor")
	}

	built.workflowInput = &input

	summary, err := built.summarize(ctx, built.renderTranscript(
		workflowCompactionMessages(conversation), units))
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"summarize child workflow history")
	}

	return r.workflowCompactedResult(ctx, built, conversation, input,
		units, summary)
}

func (r *Runtime) childCompactionExecution(
	ctx context.Context,
	input workflowRoundInput,
) (*models.AgentRun, error) {
	if !validWorkflowExecutionInput(input.Execution) || input.Round < 0 ||
		input.Round >= input.Execution.MaxRounds {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child compaction round input")
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child compaction requires execution storage")
	}

	execution, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child compaction execution")
	}

	if invalidChildCompactionExecution(execution) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"compaction execution is not a child")
	}

	run, err := r.store.GetAgentRun(ctx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child compaction identity")
	}

	if run.ParentTurnID != execution.RootTurnID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child compaction belongs to another root turn")
	}

	if err := r.validateChildCompactionContext(ctx, run,
		input.Execution.Context); err != nil {
		return nil, ctxerrors.Wrap(err,
			"validate saved child compaction context")
	}

	return run, nil
}

func invalidChildCompactionExecution(execution *models.Execution) bool {
	return execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil || *execution.AgentRunID == uuid.Nil
}

func (r *Runtime) restoreChildWorkflowCompactor(
	run *models.AgentRun,
	state *workflowCompactionState,
) (*compactor, error) {
	options := r.compactionOptions
	options.Store, options.Models = r.store, r.models
	options.Metrics = r.metrics
	options.Workspace = run.Workspace
	options.ModelReference, options.Prompt = state.ModelReference, state.Prompt
	options.PromptHash = state.PromptHash
	options.MaxOutputTokens = state.MaxOutputTokens
	options.Timeout = state.Timeout

	built, err := newCompactor(options, run.SessionID, run.ParentTurnID,
		cloneCompactionPlan(state.Plan), agentRunCompactionSink{
			store: r.store, sessionID: run.SessionID, agentRunID: run.ID,
		}, cloneCompactionHead(state.Head))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "construct saved child compactor")
	}

	agentRunID := run.ID
	built.agentRunID = &agentRunID

	return built, nil
}

func (r *Runtime) validateChildCompactionContext(
	ctx context.Context,
	run *models.AgentRun,
	encoded json.RawMessage,
) error {
	var conversation workflowConversation
	if err := json.Unmarshal(encoded, &conversation); err != nil {
		return ctxerrors.Wrap(err, "decode saved child compaction input")
	}

	if conversation.Root != nil ||
		conversation.ModelReference != run.ModelReference ||
		conversation.SystemPrompt != run.SystemPrompt {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child compaction prompt belongs to another conversation")
	}

	if err := conversation.Child.validateAgainst(run); err != nil {
		return ctxerrors.Wrap(err, "validate child compaction launch")
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return ctxerrors.Wrap(err, "validate child compaction plan")
	}

	if conversation.Compaction == nil {
		return nil
	}

	history, err := r.store.AgentRunHistory(ctx, run.SessionID, run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child compaction source identities")
	}

	return validateChildCompactionSources(conversation.Compaction, history)
}

func validateChildCompactionSources(
	state *workflowCompactionState,
	history *session.AgentRunHistoryResult,
) error {
	current := childCompactionHead(history.Compaction)
	if (state.Head == nil) != (current == nil) ||
		(state.Head != nil && *state.Head != *current) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child compaction predecessor changed")
	}

	byID := make(map[uuid.UUID]*models.AgentRunMessage, len(history.Messages))
	for _, message := range history.Messages {
		byID[message.ID] = message
	}

	for _, unit := range state.Plan.Units {
		first, last := byID[unit.FromMessageID], byID[unit.ToMessageID]
		if invalidChildCompactionBoundary(first, unit.FromSequence) ||
			invalidChildCompactionBoundary(last, unit.ToSequence) {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"child compaction source belongs to another history")
		}
	}

	return nil
}

func invalidChildCompactionBoundary(
	message *models.AgentRunMessage,
	sequence int64,
) bool {
	return message == nil || message.Sequence != sequence || message.Incomplete
}
