package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type childRecoveryBudget struct {
	used    int
	maximum int
}

func childConversationRounds(launch preparedChildLaunch, maximum int) int {
	if launch.maximumRounds > 0 {
		return min(maximum, launch.maximumRounds)
	}

	return maximum
}

func childRequestPrompt(
	launch preparedChildLaunch,
	task string,
	maximumRounds int,
) (elelem.Prompt, int, error) {
	remaining := maximumRounds - launch.completedRounds
	if remaining <= 0 {
		return elelem.Prompt{}, 0, ctxerrors.Wrap(
			elelem.ErrMaxRoundsExceeded,
			"child recovery budget",
		)
	}

	if launch.recoveredPrompt != nil {
		return *launch.recoveredPrompt, remaining, nil
	}

	return childPrompt(launch.systemPrompt, task, launch.skills), remaining, nil
}

func (r *Runtime) recoverChildPrompt(child recoveredChild) (
	elelem.Prompt,
	*skillLedger,
	error,
) {
	history, err := r.store.AgentRunHistory(
		child.ctx,
		child.stored.SessionID,
		child.run.ID,
	)
	if err != nil {
		return elelem.Prompt{}, nil, ctxerrors.Wrap(
			err,
			"load recovered child history",
		)
	}

	history, err = r.ensureRecoveredHistory(child, history)
	if err != nil {
		return elelem.Prompt{}, nil, ctxerrors.Wrap(
			err, "ensure recovered child task",
		)
	}

	prompt := elelem.NewPrompt().WithSystem(child.launch.systemPrompt)
	if history.Compaction != nil {
		prompt = prompt.Add(compactionSummaryMessage(
			compactionSummaryLead + history.Compaction.Summary,
		))
	}

	converted, ledgerHistory, err := convertRecoveredHistory(history.Messages)
	if err != nil {
		return elelem.Prompt{}, nil, ctxerrors.Wrap(
			err, "convert recovered child history",
		)
	}

	pending := pendingChildCalls(converted)
	for _, call := range pending {
		result, err := r.recoverChildTool(child, call)
		if err != nil {
			return elelem.Prompt{}, nil, ctxerrors.Wrap(
				err, "recover unfinished child call",
			)
		}

		converted = append(converted, result)
	}

	ledger := newSkillLedgerFromHistory(currentChildSkillHistory(
		converted, ledgerHistory,
	))

	return prompt.WithHistory(converted), ledger, nil
}

func (r *Runtime) ensureRecoveredHistory(
	child recoveredChild,
	history *session.AgentRunHistoryResult,
) (*session.AgentRunHistoryResult, error) {
	if len(history.Messages) > 0 || history.Compaction != nil {
		return history, nil
	}

	task, err := r.recoveredTask(child)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "tag recovered task")
	}

	skills, _, err := childTaskSkills(
		child.ctx, child.deps.snapshot, child.stored.Task,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve recovered task skills")
	}

	if err := child.sink.seedTask(child.ctx, task, skills); err != nil {
		return nil, ctxerrors.Wrap(err, "persist recovered task")
	}

	seeded, err := r.store.AgentRunHistory(
		child.ctx, child.stored.SessionID, child.run.ID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read recovered seed messages")
	}

	return seeded, nil
}

func (r *Runtime) recoveredTask(child recoveredChild) (string, error) {
	sender := agentMessage{
		From: messageSenderAgent, Name: r.rootAgent,
		Run: rootAgentRunReference, Text: child.stored.Task,
	}
	if child.stored.ParentAgentRunID != nil {
		parent, err := r.store.GetAgentRun(
			child.ctx, child.stored.SessionID, *child.stored.ParentAgentRunID,
		)
		if err != nil {
			return "", ctxerrors.Wrap(err, "find recovered task sender")
		}

		sender.Name = parent.Name
		sender.Run = parent.ID.String()
	}

	text, err := renderAgentMessage(sender)
	if err != nil {
		return "", ctxerrors.Wrap(err, "render recovered task")
	}

	return text, nil
}

func currentChildSkillHistory(
	messages []elelem.Message,
	history []*models.Message,
) []*models.Message {
	cutoff := 0
	calls := map[string]string{}

	for index, message := range messages {
		if index >= len(history) {
			break
		}

		for _, call := range message.ToolCalls {
			calls[call.ID] = call.Name
		}

		if message.Role != elelem.RoleTool {
			continue
		}

		if calls[message.ToolCallID] == toolNameChangeDirectory &&
			!message.ToolResultIsError {
			cutoff = index + 1
		}

		delete(calls, message.ToolCallID)
	}

	return history[cutoff:]
}

func pendingChildCalls(messages []elelem.Message) []elelem.ToolCall {
	var pending []elelem.ToolCall

	for _, message := range messages {
		if message.Role == elelem.RoleAssistant {
			pending = append(pending, message.ToolCalls...)

			continue
		}

		if message.Role != elelem.RoleTool {
			continue
		}

		for index, call := range pending {
			if call.ID == message.ToolCallID {
				pending = append(pending[:index], pending[index+1:]...)

				break
			}
		}
	}

	return pending
}

func (r *Runtime) childRoundsUsed(
	ctx context.Context,
	sessionID,
	runID uuid.UUID,
) (childRecoveryBudget, error) {
	budget := childRecoveryBudget{maximum: r.agentLimits.MaxChildTurns}

	stage := models.ModelRunStageChild
	for offset := 0; ; offset += session.MaximumPageLimit {
		page, err := r.store.ListModelRuns(
			ctx,
			sessionID,
			session.ListModelRunsOptions{
				Limit:  session.MaximumPageLimit,
				Offset: offset,
				Stage:  &stage,
			},
		)
		if err != nil {
			return budget, ctxerrors.Wrap(err, "list child model segments")
		}

		for _, segment := range page.Items {
			if segment.AgentRunID == nil || *segment.AgentRunID != runID {
				continue
			}

			if err := r.addChildSegmentBudget(
				ctx, segment, &budget,
			); err != nil {
				return budget, ctxerrors.Wrap(err, "read child segment budget")
			}
		}

		if !page.HasMore {
			return budget, nil
		}
	}
}

func (r *Runtime) addChildSegmentBudget(
	ctx context.Context,
	segment *models.ModelRun,
	budget *childRecoveryBudget,
) error {
	var settings modelAuditRequestSettings
	if err := json.Unmarshal(
		[]byte(segment.RequestSettingsJSON), &settings,
	); err != nil {
		return ctxerrors.Wrap(err, "decode original child budget")
	}

	if settings.MaxRounds > 0 {
		budget.maximum = min(budget.maximum, settings.MaxRounds)
	}

	for offset := 0; ; offset += session.MaximumPageLimit {
		calls, err := r.store.ListModelCalls(
			ctx, segment.SessionID, segment.ID,
			session.ListModelCallsOptions{
				Limit: session.MaximumPageLimit, Offset: offset,
			},
		)
		if err != nil {
			return ctxerrors.Wrap(err, "count persisted child rounds")
		}

		budget.used += len(calls.Items)
		if !calls.HasMore {
			return nil
		}
	}
}

func convertRecoveredHistory(
	records []*models.AgentRunMessage,
) ([]elelem.Message, []*models.Message, error) {
	converted := make([]elelem.Message, 0, len(records))

	ledgerHistory := make([]*models.Message, 0, len(records))
	for _, record := range records {
		message := &models.Message{
			ID:            record.ID,
			Sequence:      record.Sequence,
			Role:          record.Role,
			Content:       record.Content,
			ModelID:       record.ModelID,
			Thinking:      record.Thinking,
			ToolCallsJSON: record.ToolCallsJSON,
			ToolCallID:    record.ToolCallID,
			IsError:       record.IsError,
		}

		entry, err := elelemMessage(message)
		if err != nil {
			return nil, nil, ctxerrors.Wrap(
				err,
				"convert recovered child record",
			)
		}

		converted = append(converted, entry)
		ledgerHistory = append(ledgerHistory, message)
	}

	return converted, ledgerHistory, nil
}
