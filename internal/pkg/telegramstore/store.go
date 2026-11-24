// Package telegramstore keeps controller-owned chat and notification bindings.
package telegramstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type Store struct {
	query *repositories.Query
}

func New(handle *db.Handle) (*Store, error) {
	if handle == nil || handle.GormDB == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"Telegram store requires a database")
	}

	return &Store{query: repositories.Use(handle.GormDB)}, nil
}

func (s *Store) Chat(ctx context.Context, id int64) (
	*models.TelegramChat, error,
) {
	if id == 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid chat ID")
	}

	repo := s.query.TelegramChat

	chat, err := repo.WithContext(ctx).Where(repo.ChatID.Eq(id)).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read Telegram chat")
	}

	return chat, nil
}

func (s *Store) SaveChat(ctx context.Context, chat *models.TelegramChat) error {
	if chat == nil || chat.ChatID == 0 || chat.SessionID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid Telegram chat")
	}

	stored := *chat

	stored.UpdatedAt = time.Now().UTC()
	if err := s.query.TelegramChat.WithContext(ctx).Save(&stored); err != nil {
		return ctxerrors.Wrap(err, "save Telegram chat")
	}

	return nil
}

func (s *Store) BindNotification(
	ctx context.Context, chatID, messageID int64, runID uuid.UUID,
) error {
	if chatID == 0 || messageID <= 0 || runID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid Telegram notification binding")
	}
	// Create never rebinds an existing chat/message pair to another run.
	if err := s.query.TelegramMessage.WithContext(ctx).Create(
		&models.TelegramMessage{
			ChatID: chatID, MessageID: messageID, CronRunID: &runID,
		},
	); err != nil {
		return ctxerrors.Wrap(err, "save Telegram notification binding")
	}

	return nil
}

func (s *Store) ReplySession(
	ctx context.Context, chatID, messageID int64,
) (uuid.UUID, error) {
	if chatID == 0 || messageID <= 0 {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid Telegram reply binding")
	}

	messages := s.query.TelegramMessage

	bound, err := messages.WithContext(ctx).
		Where(messages.ChatID.Eq(chatID), messages.MessageID.Eq(messageID)).
		First()
	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "read Telegram reply binding")
	}

	if bound.CronRunID == nil {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrNotFound,
			"Telegram message has no scheduled run")
	}

	runs := s.query.CronRun

	run, err := runs.WithContext(ctx).Where(runs.ID.Eq(*bound.CronRunID)).
		First()
	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "read Telegram reply run")
	}

	if run.SessionID == nil || *run.SessionID == uuid.Nil {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrNotFound,
			"scheduled run has no session")
	}

	return *run.SessionID, nil
}
