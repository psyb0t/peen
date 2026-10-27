package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) workflowRootCompactRound(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	err := r.validateRootWorkflowModelInput(ctx, prepared, input)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"validate root compaction input")
	}

	if _, err := rootCompactionLease(ctx); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"validate root compaction activity lease")
	}

	units, room, err := selectWorkflowCompactionPrefix(r.models, input)
	if errors.Is(err, ErrCompactionUnavailable) {
		logNothingToSummarize(ctx, err)

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}

	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"select root compaction prefix")
	}

	if units == 0 {
		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}

	var conversation workflowConversation

	err = json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"decode root compaction context")
	}

	built, err := r.restoreWorkflowCompactor(prepared, conversation.Compaction)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"restore root summarizer")
	}

	built.workflowInput = &input
	built.room = room

	summary, err := built.summarize(ctx, built.renderTranscript(
		workflowCompactionMessages(conversation), units))
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"summarize root workflow history")
	}

	return r.workflowCompactedResult(ctx, built, conversation, input,
		units, summary)
}

func (r *Runtime) workflowCompactedResult(
	ctx context.Context,
	built *compactor,
	conversation workflowConversation,
	input workflowRoundInput,
	units int,
	summary compactionSummary,
) (workflowCompactionResult, error) {
	text := strings.TrimSpace(summary.Text)
	if text == "" {
		return workflowCompactionResult{}, ctxerrors.Wrap(
			ErrCompactionEmptySummary, "summarizer returned no summary")
	}

	outcome := built.summaryOutcome(units, summary)

	lease, err := rootCompactionLease(ctx)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"read root compaction activity lease")
	}

	compaction := built.compactionInput(units, text, summary)
	compaction.ID = lease.ID
	conversation.Messages = directoryHistory(built.replacePrefix(
		workflowCompactionMessages(conversation), units,
		compactionSummaryLead+text))

	built.plan.advance(units)
	built.active = &compactionHead{
		ID: compaction.ID, FromMessageID: compaction.FromMessageID,
		FromSequence:       compaction.FromSequence,
		SourceMessageCount: compaction.SourceMessageCount,
		SummaryTokens:      compaction.SummaryTokenCount,
	}

	conversation.Compaction = snapshotWorkflowCompaction(built)
	if err := r.checkCompactedWorkflowBudget(conversation, input); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"recount root compaction result")
	}

	if err := compactWorkflowSkillState(conversation.Runtime,
		compaction.ToSequence); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"compact saved root skill deliveries")
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"encode root compaction result")
	}

	outcome.log(ctx)

	return workflowCompactionResult{
		Context:    encoded,
		Compaction: &compaction,
	}, nil
}

func rootCompactionLease(ctx context.Context) (session.ActivityLease, error) {
	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return session.ActivityLease{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "root compaction activity lease")
	}

	if lease.ID == uuid.Nil || lease.GenerationID == uuid.Nil ||
		lease.Attempt < 1 {
		return session.ActivityLease{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "invalid root compaction lease")
	}

	return lease, nil
}

func (r *Runtime) checkCompactedWorkflowBudget(
	conversation workflowConversation,
	input workflowRoundInput,
) error {
	encoded, err := json.Marshal(conversation)
	if err != nil {
		return ctxerrors.Wrap(err, "encode compacted budget input")
	}

	input.Execution.Context = encoded

	request, _, err := workflowModelRequest(r.models, input)
	if err != nil {
		return ctxerrors.Wrap(err, "build compacted budget request")
	}

	over, err := request.IsTokenLimitReached()
	if err != nil {
		return ctxerrors.Wrap(err, "count compacted workflow request")
	}

	if over {
		return ctxerrors.Wrap(ErrCompactionInsufficient,
			"root summary does not fit the request budget")
	}

	return nil
}

func compactWorkflowSkillState(
	state *workflowRuntimeState,
	throughSequence int64,
) error {
	if state == nil || state.Skills == nil {
		return nil
	}

	ledger, err := restoreWorkflowSkills(state.Skills)
	if err != nil {
		return ctxerrors.Wrap(err, "restore skills for compaction")
	}

	ledger.forgetThrough(throughSequence)
	state.Skills = ledger.workflowState()

	return nil
}

func (c *rootWorkflowCallbacks) compactRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"acquire root compaction callbacks")
	}
	defer release()

	if err := c.refresh(ctx, shared, input.Execution); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"refresh root compaction callbacks")
	}

	return c.runtime.workflowRootCompactRound(ctx, shared.prepared, input)
}
