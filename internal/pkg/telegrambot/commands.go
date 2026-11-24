package telegrambot

import (
	"context"
	"encoding/base64"
	"path"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/telegram"
)

func (e *Engine) command(
	ctx context.Context, message telegram.Message, chat *models.TelegramChat,
	command, argument string,
) error {
	switch command {
	case commandNew:
		return e.sendText(ctx, message.Chat.ID, replyNew)
	case commandModel:
		return e.modelCommand(ctx, chat, argument)
	case commandEffort:
		return e.effortCommand(ctx, chat, argument)
	case commandSystem:
		return e.systemCommand(ctx, chat, argument)
	case commandFetch:
		return e.fetchCommand(ctx, message, chat, argument)
	default:
		return e.sendText(ctx, message.Chat.ID, replyUnknownCommand)
	}
}

func (e *Engine) modelCommand(
	ctx context.Context, chat *models.TelegramChat, argument string,
) error {
	if argument == "" {
		models := strings.Join(e.options.Models, "\n")

		return e.sendText(ctx, chat.ChatID, models)
	}

	if !slices.Contains(e.options.Models, argument) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram model must be configured")
	}

	chat.Model = argument

	return e.saveSetting(ctx, chat)
}

func (e *Engine) effortCommand(
	ctx context.Context, chat *models.TelegramChat, argument string,
) error {
	if argument == "" {
		return e.sendText(ctx, chat.ChatID,
			"minimal, low, medium, high, xhigh, max")
	}

	switch argument {
	case elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh,
		elelem.ReasoningEffortMax:
		chat.ReasoningEffort = argument
	default:
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"unsupported Telegram reasoning effort")
	}

	return e.saveSetting(ctx, chat)
}

func (e *Engine) systemCommand(
	ctx context.Context, chat *models.TelegramChat, argument string,
) error {
	if len(argument) > e.options.MaxSystemPromptBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram system prompt is too large")
	}

	if argument == "" {
		return e.sendText(ctx, chat.ChatID, "/system <text> or /system reset")
	}

	chat.SystemPrompt = argument
	if argument == resetArgument {
		chat.SystemPrompt = ""
	}

	return e.saveSetting(ctx, chat)
}

func (e *Engine) saveSetting(
	ctx context.Context, chat *models.TelegramChat,
) error {
	if err := e.options.Store.SaveChat(ctx, chat); err != nil {
		return ctxerrors.Wrap(err, "save Telegram setting")
	}

	return e.sendText(ctx, chat.ChatID, "Setting saved.")
}

func (e *Engine) fetchCommand(
	ctx context.Context, message telegram.Message, chat *models.TelegramChat,
	argument string,
) error {
	id, err := e.messageSession(ctx, message, chat)
	if err != nil {
		return err
	}

	file, err := e.options.Sessions.ReadWorkspaceFile(ctx, id, argument)
	if err != nil {
		return ctxerrors.Wrap(err, "read Telegram download")
	}

	if file.Size > e.options.MaxFileBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram download is too large")
	}

	data, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
	if err != nil {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"workspace file encoding is invalid")
	}

	if _, err := e.options.Client.SendDocument(ctx, message.Chat.ID,
		path.Base(file.Path), data); err != nil {
		return ctxerrors.Wrap(err, "send workspace document")
	}

	return nil
}
