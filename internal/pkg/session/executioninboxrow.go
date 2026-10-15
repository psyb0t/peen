package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) deliverInboxRow(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	row *models.ExecutionInboxMessage,
	rootRequestID uuid.UUID,
) ([]MessageInput, []EventInput, error) {
	if row.ExecutionID != execution.ID {
		return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"inbox request belongs to another execution")
	}

	if row.State == models.InboxMessageStateDelivered {
		if row.DeliveredActivityID == nil ||
			*row.DeliveredActivityID != activity.ID {
			return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
				"inbox request was delivered by another model activity")
		}

		return nil, nil, nil
	}

	messages, err := inboxTranscriptMessages(row)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "decode delivered inbox parts")
	}

	events, err := inboxProtocolEvents(row, EventTypeUserMessageDelivered)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "build inbox delivery event")
	}

	skillEvents, err := inboxSkillEvents(row, rootRequestID)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err,
			"build inbox skill delivery events")
	}

	events = append(events, skillEvents...)

	now := s.now()
	row.State = models.InboxMessageStateDelivered
	row.DeliveredActivityID = &activity.ID

	row.DeliveredAt = &now
	if err := tx.ExecutionInboxMessage.WithContext(ctx).Save(row); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "save inbox delivery identity")
	}

	return messages, events, nil
}

func inboxTranscriptMessages(
	row *models.ExecutionInboxMessage,
) ([]MessageInput, error) {
	var parts []ExecutionInboxPart
	if err := json.Unmarshal([]byte(row.PartsJSON), &parts); err != nil {
		return nil, ctxerrors.Wrap(err, "decode accepted inbox skills")
	}

	messages := make([]MessageInput, 0, len(parts)+1)

	messages = append(messages, MessageInput{
		Role: models.MessageRoleUser, Content: row.Message,
		Workspace: row.WorkingDirectory,
	})
	for _, part := range parts {
		messages = append(messages, MessageInput{
			Role: models.MessageRoleUser, Content: part.Content,
			Workspace: row.WorkingDirectory,
			Injected:  true, InjectedKind: models.InjectedKindSkill,
		})
	}

	return messages, nil
}
