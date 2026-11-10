package session

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// closeChildToolCalls gives every tool call a cancelled child started and
// never finished a tool message and an agent.run.tool.result event, both
// marked as errors with StoppedToolResultContent, so the child's events show
// each agent.run.tool.use closed before agent.run.cancelled. It runs inside
// the transaction that cancels the child.
func (s *Store) closeChildToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
) error {
	open, err := readOpenChildToolCalls(ctx, tx, run)
	if err != nil {
		return err
	}

	if len(open) == 0 {
		return nil
	}

	messages := make([]AgentRunMessageInput, 0, len(open))
	payloads := make([][]byte, 0, len(open))

	for _, call := range open {
		payload, encodeErr := json.Marshal(toolResultEventPayload{
			CallID: call.callID, Name: call.name,
			Content: StoppedToolResultContent, IsError: true,
		})
		if encodeErr != nil {
			return ctxerrors.Wrap(encodeErr,
				"encode stopped child tool result event")
		}

		payloads = append(payloads, payload)
		messages = append(messages, AgentRunMessageInput{
			Role:       models.MessageRoleTool,
			Content:    StoppedToolResultContent,
			ToolCallID: call.callID,
			IsError:    true,
		})
	}

	if _, err := s.createAgentRunMessages(ctx, tx, run.SessionID, run.ID,
		messages); err != nil {
		return ctxerrors.Wrap(err, "save stopped child tool messages")
	}

	for _, payload := range payloads {
		if err := s.appendChildToolEvent(ctx, tx, run,
			eventTypeAgentRunToolResult, payload); err != nil {
			return err
		}
	}

	return nil
}

// appendChildToolEvent stores one event on a child run that has already been
// settled, which AppendAgentRunEvent would refuse.
func (s *Store) appendChildToolEvent(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
	eventType string,
	payload []byte,
) error {
	sequence, err := nextAgentRunEventSequence(ctx, tx, run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "allocate stopped child tool event sequence")
	}

	feedSequence, err := s.allocateFeedSequences(ctx, tx, run.SessionID, 1)
	if err != nil {
		return ctxerrors.Wrap(err,
			"allocate stopped child tool event feed sequence")
	}

	event := &models.AgentRunEvent{
		ID: s.newID(), SessionID: run.SessionID, AgentRunID: run.ID,
		Sequence: sequence, FeedSequence: feedSequence, EventType: eventType,
		PayloadJSON: string(payload), CreatedAt: s.now(),
	}
	if err := tx.AgentRunEvent.WithContext(ctx).Create(event); err != nil {
		return ctxerrors.Wrap(err, "save stopped child tool event")
	}

	repository := tx.AgentRun

	_, err = repository.WithContext(ctx).Where(
		repository.ID.Eq(run.ID), repository.SessionID.Eq(run.SessionID),
	).UpdateSimple(repository.EventCount.Add(1))
	if err != nil {
		return ctxerrors.Wrap(err, "count stopped child tool event")
	}

	return nil
}

// readOpenChildToolCalls lists the child's tool calls that have an
// agent.run.tool.use event and neither an agent.run.tool.result event nor a
// tool message, in the order they started.
func readOpenChildToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
) ([]openToolCall, error) {
	event := tx.AgentRunEvent

	rows, err := event.WithContext(ctx).Select(event.EventType,
		event.PayloadJSON,
	).Where(
		event.SessionID.Eq(run.SessionID),
		event.AgentRunID.Eq(run.ID),
		event.EventType.In(eventTypeAgentRunToolUse,
			eventTypeAgentRunToolResult),
	).Order(event.Sequence).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read cancelled child tool events")
	}

	message := tx.AgentRunMessage

	stored, err := message.WithContext(ctx).Select(message.ToolCallID).Where(
		message.SessionID.Eq(run.SessionID),
		message.AgentRunID.Eq(run.ID),
		message.Role.Eq(string(models.MessageRoleTool)),
	).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read cancelled child tool messages")
	}

	closed := make(map[string]bool, len(stored))
	for _, row := range stored {
		closed[row.ToolCallID] = true
	}

	entries := make([]toolEventEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, toolEventEntry{
			eventType: row.EventType, payload: row.PayloadJSON,
		})
	}

	return openCallsFromEvents(entries, closed, eventTypeAgentRunToolResult)
}
