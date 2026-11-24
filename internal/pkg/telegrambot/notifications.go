package telegrambot

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/telegram"
)

func (e *Engine) NotifyCron(
	ctx context.Context, notification control.CronNotification,
) error {
	if _, allowed := e.options.Policy.Chats[notification.ChatID]; !allowed ||
		notification.RunID == uuid.Nil || notification.SessionID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"scheduled notification is not allowed")
	}

	if err := e.options.Sessions.ValidateWorkspaceSession(ctx,
		notification.SessionID); err != nil {
		return ctxerrors.Wrap(err, "validate scheduled notification session")
	}

	e.mutex.Lock()
	if e.closed {
		e.mutex.Unlock()

		return ctxerrors.Wrap(commerr.ErrCancelled, "Telegram is stopping")
	}

	e.workers.Add(1)

	e.mutex.Unlock()
	defer e.workers.Done()

	for _, chunk := range splitText(notification.Text) {
		message, err := e.options.Client.SendMessage(ctx,
			notification.ChatID, chunk)
		if err != nil {
			return ctxerrors.Wrap(err, "send scheduled notification")
		}

		if err := e.bindNotification(ctx, notification, message); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) bindNotification(
	ctx context.Context, notification control.CronNotification,
	message telegram.Message,
) error {
	if message.Chat.ID != notification.ChatID || message.ID <= 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"scheduled notification response does not match its chat")
	}

	if err := e.options.Store.BindNotification(ctx, notification.ChatID,
		message.ID, notification.RunID); err != nil {
		return ctxerrors.Wrap(err, "bind scheduled notification reply")
	}

	return nil
}
