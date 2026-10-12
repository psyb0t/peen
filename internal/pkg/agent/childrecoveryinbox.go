package agent

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type recoveredInboxEntry struct {
	event        *models.AgentRunEvent
	text         string
	isDelivered  bool
	acceptanceID *uuid.UUID
}

func (r *Runtime) restoreChildInbox(child recoveredChild) error {
	events, err := r.recoveryEvents(child)
	if err != nil {
		return ctxerrors.Wrap(err, "read recovered inbox events")
	}

	entries, err := recoveredInboxEntries(events)
	if err != nil {
		return ctxerrors.Wrap(err, "reconcile recovered inbox events")
	}

	if len(entries) == 0 {
		return nil
	}

	messages, err := r.recoveryMessages(child)
	if err != nil {
		return ctxerrors.Wrap(err, "read inbox transcript checkpoints")
	}

	used := map[uuid.UUID]bool{}
	for _, entry := range entries {
		// A crash can separate the transcript write from its delivered event.
		// Match each row once, including acknowledged messages.
		isCheckpointed := matchInboxCheckpoint(messages, entry, used)
		if entry.isDelivered || isCheckpointed {
			continue
		}

		if err := child.run.inbox.queue.EnqueueText(entry.text); err != nil {
			return ctxerrors.Wrap(err, "restore accepted child message")
		}

		child.run.inbox.pending = append(child.run.inbox.pending, entry.text)
		child.run.inbox.pendingIDs = append(
			child.run.inbox.pendingIDs, entry.acceptanceID,
		)
	}

	return nil
}

func recoveredInboxEntries(
	events []*models.AgentRunEvent,
) ([]recoveredInboxEntry, error) {
	entries := []recoveredInboxEntry{}

	for _, event := range events {
		isQueued := event.EventType == EventTypeAgentRunMessageQueued

		isDelivered := event.EventType == EventTypeAgentRunMessageDelivered ||
			event.EventType == EventTypeAgentRunMessageUndelivered
		if !isQueued && !isDelivered {
			continue
		}

		var payload agentInboxMessagePayload
		if err := json.Unmarshal(
			[]byte(event.PayloadJSON), &payload,
		); err != nil {
			return nil, ctxerrors.Wrap(err, "decode recovered inbox checkpoint")
		}

		if isQueued {
			entries = append(entries, recoveredInboxEntry{
				event: event, text: payload.Message,
				acceptanceID: payload.AcceptanceID,
			})

			continue
		}

		index := matchingInboxAcceptance(entries, payload)
		if index < 0 {
			return nil, ctxerrors.Wrap(
				commerr.ErrInvalidState,
				"inbox delivery has no matching acceptance",
			)
		}

		entries[index].isDelivered = true
	}

	return entries, nil
}

func matchingInboxAcceptance(
	entries []recoveredInboxEntry,
	payload agentInboxMessagePayload,
) int {
	for index, entry := range entries {
		if entry.isDelivered || entry.text != payload.Message {
			continue
		}

		isOtherAcceptance := payload.AcceptanceID != nil &&
			(entry.acceptanceID == nil ||
				*entry.acceptanceID != *payload.AcceptanceID)
		if isOtherAcceptance {
			continue
		}

		return index
	}

	return -1
}

func matchInboxCheckpoint(
	messages []*models.AgentRunMessage,
	entry recoveredInboxEntry,
	used map[uuid.UUID]bool,
) bool {
	for _, message := range messages {
		if used[message.ID] || message.Role != models.MessageRoleUser ||
			message.Content != entry.text ||
			message.CreatedAt.Before(entry.event.CreatedAt) {
			continue
		}

		used[message.ID] = true

		return true
	}

	return false
}

func (r *Runtime) recoveryMessages(
	child recoveredChild,
) ([]*models.AgentRunMessage, error) {
	var messages []*models.AgentRunMessage

	for offset := 0; ; offset += session.MaximumPageLimit {
		page, err := r.store.ListAgentRunMessages(
			child.ctx, child.stored.SessionID, child.run.ID,
			session.ListAgentRunMessagesOptions{
				Limit: session.MaximumPageLimit, Offset: offset,
			},
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read recovered child messages")
		}

		messages = append(messages, page.Items...)
		if !page.HasMore {
			return messages, nil
		}
	}
}

func (r *Runtime) recoveryEvents(
	child recoveredChild,
) ([]*models.AgentRunEvent, error) {
	var events []*models.AgentRunEvent

	for cursor := int64(0); ; {
		page, err := r.store.ListAgentRunEvents(
			child.ctx, child.stored.SessionID, child.run.ID,
			session.ListAgentRunEventsOptions{
				Limit: session.MaximumPageLimit, Cursor: cursor,
			},
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read recovered child events")
		}

		events = append(events, page.Items...)
		cursor = page.NextCursor

		if !page.HasMore {
			return events, nil
		}
	}
}
