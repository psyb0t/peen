package controlcore

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/psyb0t/peen/internal/pkg/telegrambot"
	"github.com/psyb0t/peen/internal/pkg/telegramstore"
)

func prepareTelegram(core *control.Core) error {
	if !core.Config.Telegram {
		return nil
	}

	policy, err := core.Config.TelegramPolicy()
	if err != nil {
		return ctxerrors.Wrap(err, "prepare Telegram dependencies")
	}

	client, err := telegram.New(core.Config.TelegramToken)
	if err != nil {
		return ctxerrors.Wrap(err, "create Telegram client")
	}

	store, err := telegramstore.New(core.Handle)
	if err != nil {
		return ctxerrors.Wrap(err, "prepare Telegram dependencies")
	}

	conversation, err := control.NewConversation(core.Sessions,
		core.Turns, core.Feed)
	if err != nil {
		return ctxerrors.Wrap(err, "prepare Telegram dependencies")
	}

	space, err := core.AgentSpaces.Select(core.Config.TelegramAgentSpace)
	if err != nil {
		return ctxerrors.Wrap(err, "select Telegram agent space")
	}

	catalogue := core.Runtime.ListModels()

	models := make([]string, 0, len(catalogue.Models))
	for _, model := range catalogue.Models {
		models = append(models, model.Name)
	}

	engine, err := telegrambot.New(telegrambot.Options{
		Client: client, Runner: conversation, Sessions: core.Sessions,
		SessionReader: core.Store, Store: store, Policy: policy,
		Workspace: core.Config.TelegramWorkspace, AgentSpace: space.Name,
		Models: models, MaxMessageBytes: core.Config.MaxMessageBytes,
		MaxSystemPromptBytes: core.Config.MaxSystemPromptBytes,
		MaxFileBytes:         core.Config.MaxFileAPIBytes,
		MaxImageBytes:        core.Config.MaxImageBytes,
		MaxImages:            core.Config.MaxImagesPerMessage,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "prepare Telegram controller")
	}

	core.Telegram = engine

	return nil
}
