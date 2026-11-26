package telegrambot

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/telegram"
)

func (e *Engine) handleMessage(
	ctx context.Context, message telegram.Message,
) error {
	command, argument, _ := strings.Cut(strings.TrimSpace(message.Text), " ")
	argument = strings.TrimSpace(argument)

	if command == commandHelp {
		return e.sendText(ctx, message.Chat.ID, helpText)
	}

	chat, err := e.chat(ctx, message.Chat.ID)
	if err != nil {
		return err
	}

	if strings.HasPrefix(command, "/") {
		return e.command(ctx, message, chat, command, argument)
	}

	sessionID, err := e.messageSession(ctx, message, chat)
	if err != nil {
		return err
	}

	request, err := e.messageRequest(ctx, sessionID, message, chat)
	if err != nil {
		return err
	}

	result, err := e.options.Runner.Run(ctx, control.ConversationRequest{
		SessionID: sessionID, Message: request,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "run Telegram conversation")
	}

	if result == nil {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"Telegram conversation has no result")
	}

	text := result.Text
	if text == "" {
		text = replyEmpty
	}

	return e.sendText(ctx, message.Chat.ID, text)
}

func applySettings(chat *models.TelegramChat, request *agent.MessageRequest) {
	if chat.Model != "" {
		request.Model = &chat.Model
	}

	if chat.ReasoningEffort != "" {
		request.ReasoningEffort = &chat.ReasoningEffort
	}

	// Appending keeps Peen's base prompt, including the line that marks tool
	// output as untrusted, in every chat turn.
	if chat.SystemPrompt != "" {
		request.SystemPrompt = &agent.MessageSystemPrompt{
			Content: chat.SystemPrompt, Mode: agent.PromptModeAppend,
		}
	}
}

func (e *Engine) messageSession(
	ctx context.Context, message telegram.Message, chat *models.TelegramChat,
) (uuid.UUID, error) {
	if message.ReplyToMessage == nil {
		return chat.SessionID, nil
	}

	id, err := e.options.Store.ReplySession(ctx, message.Chat.ID,
		message.ReplyToMessage.ID)
	if errors.Is(err, commerr.ErrNotFound) {
		return chat.SessionID, nil
	}

	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "resolve scheduled reply")
	}

	if err := e.options.Sessions.ValidateWorkspaceSession(ctx, id); err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "validate scheduled reply session")
	}

	return id, nil
}

func (e *Engine) chat(
	ctx context.Context, id int64,
) (*models.TelegramChat, error) {
	workspace, err := e.chatWorkspace(id)
	if err != nil {
		return nil, err
	}

	chat, err := e.options.Store.Chat(ctx, id)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "load chat conversation")
	}

	if chat != nil {
		if err := e.validateChat(ctx, chat.SessionID, workspace); err != nil {
			return nil, err
		}

		return chat, nil
	}

	opened, err := e.options.Sessions.Open(ctx, workspace,
		e.options.AgentSpace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open Telegram chat session")
	}

	chat = &models.TelegramChat{ChatID: id, SessionID: opened.Session.ID}
	if err := e.options.Store.SaveChat(ctx, chat); err != nil {
		return nil, ctxerrors.Wrap(err, "persist chat conversation")
	}

	return chat, nil
}

func (e *Engine) validateChat(
	ctx context.Context, id uuid.UUID, workspace string,
) error {
	if err := e.options.Sessions.ValidateWorkspaceSession(ctx, id); err != nil {
		return ctxerrors.Wrap(err, "validate chat session policy")
	}

	stored, err := e.options.SessionReader.Get(ctx, id)
	if err != nil {
		return ctxerrors.Wrap(err, "read chat session")
	}

	if stored.Workspace != workspace ||
		stored.AgentSpace != e.options.AgentSpace {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"chat session no longer matches its configuration")
	}

	return nil
}

func (e *Engine) sendText(
	ctx context.Context, chatID int64, text string,
) error {
	for _, chunk := range splitText(text) {
		_, err := e.options.Client.SendMessage(ctx, chatID, chunk)
		if err != nil {
			return ctxerrors.Wrap(err, "send Telegram reply")
		}
	}

	return nil
}

// Telegram counts Unicode characters rather than UTF-8 bytes.
func splitText(text string) []string {
	remaining := []rune(strings.ToValidUTF8(text, ""))

	chunks := make([]string, 0, len(remaining)/textLimit+1)
	for len(remaining) > textLimit {
		end := textLimit
		for index := textLimit - 1; index > 0; index-- {
			if remaining[index] == '\n' {
				end = index + 1

				break
			}
		}

		chunks = append(chunks, string(remaining[:end]))
		remaining = remaining[end:]
	}

	if len(remaining) != 0 {
		chunks = append(chunks, string(remaining))
	}

	return chunks
}

func (e *Engine) messageRequest(
	ctx context.Context, sessionID uuid.UUID, message telegram.Message,
	chat *models.TelegramChat,
) (agent.MessageRequest, error) {
	request := agent.MessageRequest{Message: message.Text}
	if message.Caption != "" {
		request.Message = message.Caption
	}

	if err := e.attachUploads(ctx, sessionID, message, &request); err != nil {
		return agent.MessageRequest{}, err
	}

	if strings.TrimSpace(request.Message) == "" {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"Telegram message has no supported content")
	}

	if len(request.Message) > e.options.MaxMessageBytes {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"expanded Telegram message is too large")
	}

	applySettings(chat, &request)

	return request, nil
}
