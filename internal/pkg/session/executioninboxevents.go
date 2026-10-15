package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) inboxAcceptanceEvents(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	message *models.ExecutionInboxMessage,
) ([]EventInput, error) {
	turn, err := s.findRunningTurn(ctx, tx, lease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read inbox acceptance worker")
	}

	events, err := inboxProtocolEvents(message,
		EventTypeUserMessageCreated, EventTypeUserMessageQueued)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode inbox acceptance events")
	}

	for index := range events {
		events[index].WorkerGenerationID = turn.WorkerGenerationID
	}

	return events, nil
}

func inboxProtocolEvents(
	message *models.ExecutionInboxMessage,
	kinds ...string,
) ([]EventInput, error) {
	payload := executionInboxMessagePayload{Message: message.Message}
	if message.SourceEventID != nil {
		payload.SourceEventID = message.SourceEventID.String()
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode inbox message event")
	}

	events := make([]EventInput, 0, len(kinds))
	for _, kind := range kinds {
		events = append(events, EventInput{
			RequestID: message.RequestID, EventType: kind,
			PayloadJSON: string(encoded),
		})
	}

	return events, nil
}

func inboxSkillEvents(
	message *models.ExecutionInboxMessage,
	rootRequestID uuid.UUID,
) ([]EventInput, error) {
	var parts []ExecutionInboxPart
	if err := json.Unmarshal([]byte(message.PartsJSON), &parts); err != nil {
		return nil, ctxerrors.Wrap(err, "decode delivered skill events")
	}

	events := make([]EventInput, 0, len(parts))
	for _, part := range parts {
		payload := struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}{Name: part.SkillName, Content: part.Content}

		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode delivered skill event")
		}

		events = append(events, EventInput{
			RequestID: rootRequestID, EventType: EventTypeSkillInjected,
			PayloadJSON: string(encoded),
		})
	}

	return events, nil
}
