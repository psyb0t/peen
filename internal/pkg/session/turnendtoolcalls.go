package session

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// openToolCall is a tool call of an ended turn that was started and never
// got a result.
type openToolCall struct {
	callID string
	name   string
}

// toolCallEventPayload is the part of a tool.use or tool.result event that
// names the call.
type toolCallEventPayload struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
}

// toolResultEventPayload mirrors the tool.result event the runtime stores.
type toolResultEventPayload struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

// closesOpenToolCalls reports whether an end leaves the turn's running tool
// calls with a stopped result. A crash leaves them to the interrupted notice.
func closesOpenToolCalls(end TurnEnd) bool {
	return end.State == models.TurnStateCancelled ||
		end.State == models.TurnStateFailed
}

// closeOpenToolCalls gives every tool call the turn started and never
// finished a tool message and a tool.result event, both marked as errors with
// StoppedToolResultContent. A client and the transcript then see each tool.use
// closed before the turn's terminal frame. It runs inside a feed transaction.
func (s *Store) closeOpenToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
) error {
	open, err := readOpenToolCalls(ctx, tx, turn)
	if err != nil {
		return err
	}

	if len(open) == 0 {
		return nil
	}

	messages, eventInputs, err := stoppedToolResults(turn, open)
	if err != nil {
		return err
	}

	lease := Lease{SessionID: turn.SessionID, TurnID: turn.ID}

	record, err := s.findSessionWithQuery(ctx, tx, turn.SessionID)
	if err != nil {
		return err
	}

	appended, err := s.appendMessages(ctx, tx, lease, record.MessageCount,
		turn.Workspace, messages)
	if err != nil {
		return err
	}

	if err := s.appendEvents(ctx, tx, lease, eventInputs); err != nil {
		return err
	}

	repository := tx.Session
	now := s.now()

	if _, err := repository.WithContext(ctx).
		Where(repository.ID.Eq(turn.SessionID)).
		UpdateSimple(
			repository.MessageCount.Add(appended.messageCount),
			repository.LastMessageAt.Value(now),
			repository.UpdatedAt.Value(now),
		); err != nil {
		return ctxerrors.Wrap(err, "update session stopped tool metadata")
	}

	return nil
}

// stoppedToolResults builds the tool message and the tool.result event that
// close each open call with StoppedToolResultContent.
func stoppedToolResults(
	turn *models.Turn,
	open []openToolCall,
) ([]MessageInput, []EventInput, error) {
	messages := make([]MessageInput, 0, len(open))
	eventInputs := make([]EventInput, 0, len(open))

	for _, call := range open {
		payload, err := json.Marshal(toolResultEventPayload{
			CallID: call.callID, Name: call.name,
			Content: StoppedToolResultContent, IsError: true,
		})
		if err != nil {
			return nil, nil, ctxerrors.Wrap(err,
				"encode stopped tool result event")
		}

		messages = append(messages, MessageInput{
			Role:       models.MessageRoleTool,
			Content:    StoppedToolResultContent,
			ToolCallID: call.callID,
			IsError:    true,
		})
		eventInputs = append(eventInputs, EventInput{
			RequestID:          turn.RequestID,
			WorkerGenerationID: turn.WorkerGenerationID,
			EventType:          eventTypeToolResult,
			PayloadJSON:        string(payload),
		})
	}

	return messages, eventInputs, nil
}

// readOpenToolCalls lists the root tool calls of the turn that have a
// tool.use event and neither a tool.result event nor a tool message, in the
// order they started.
func readOpenToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
) ([]openToolCall, error) {
	event := tx.Event

	rows, err := event.WithContext(ctx).Select(event.EventType,
		event.PayloadJSON,
	).Where(
		event.SessionID.Eq(turn.SessionID),
		event.TurnID.Eq(turn.ID),
		event.ParentToolCallID.Eq(""),
		event.EventType.In(eventTypeToolUse, eventTypeToolResult),
	).Order(event.Sequence).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read ended turn tool events")
	}

	closed, err := readClosedToolCalls(ctx, tx, turn)
	if err != nil {
		return nil, err
	}

	entries := make([]toolEventEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, toolEventEntry{
			eventType: row.EventType, payload: row.PayloadJSON,
		})
	}

	return openCallsFromEvents(entries, closed, eventTypeToolResult)
}

// toolEventEntry is the type and payload of one stored tool event.
type toolEventEntry struct {
	eventType string
	payload   string
}

// openCallsFromEvents walks tool events in order and returns the calls that
// started and never closed, either by a result event of resultType or by an
// ID already in closed. closed is updated.
func openCallsFromEvents(
	entries []toolEventEntry,
	closed map[string]bool,
	resultType string,
) ([]openToolCall, error) {
	var started []openToolCall

	for _, entry := range entries {
		var payload toolCallEventPayload
		if err := json.Unmarshal([]byte(entry.payload), &payload); err != nil {
			return nil, ctxerrors.Wrap(err, "decode ended tool event")
		}

		if entry.eventType == resultType {
			closed[payload.CallID] = true

			continue
		}

		started = append(started, openToolCall{
			callID: payload.CallID, name: payload.Name,
		})
	}

	var open []openToolCall

	for _, call := range started {
		if closed[call.callID] {
			continue
		}

		closed[call.callID] = true

		open = append(open, call)
	}

	return open, nil
}

// readClosedToolCalls returns the IDs of the turn's calls that already have a
// tool message.
func readClosedToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
) (map[string]bool, error) {
	message := tx.Message

	rows, err := message.WithContext(ctx).Select(message.ToolCallID).Where(
		message.SessionID.Eq(turn.SessionID),
		message.TurnID.Eq(turn.ID),
		message.Role.Eq(string(models.MessageRoleTool)),
	).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read ended turn tool messages")
	}

	closed := make(map[string]bool, len(rows))
	for _, row := range rows {
		closed[row.ToolCallID] = true
	}

	return closed, nil
}
