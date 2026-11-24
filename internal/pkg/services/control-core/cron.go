package controlcore

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronscheduler"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
)

func prepareCron(ctx context.Context, core *control.Core) error {
	store, err := cronstore.New(core.Handle)
	if err != nil {
		return ctxerrors.Wrap(err, "create cron history store")
	}

	jobs, err := configuredCronJobs(core)
	if err != nil {
		return err
	}

	conversation, err := control.NewConversation(core.Sessions,
		core.Turns, core.Feed)
	if err != nil {
		return ctxerrors.Wrap(err, "create scheduled conversation runner")
	}

	scheduler, err := cronscheduler.New(ctx, cronscheduler.Options{
		Jobs: jobs, Store: store, Runner: conversation,
		Sessions: core.Sessions, Turns: core.Store,
		MaxMessageBytes: core.Config.MaxMessageBytes,
		Notifier:        core.Telegram,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "initialize cron scheduler")
	}

	core.Cron = scheduler

	return nil
}

func configuredCronJobs(core *control.Core) ([]cronconfig.Job, error) {
	if !core.Config.Cron {
		return []cronconfig.Job{}, nil
	}

	catalogue := core.Runtime.ListModels()

	models := make([]string, 0, len(catalogue.Models))
	for _, model := range catalogue.Models {
		models = append(models, model.Name)
	}

	jobs, err := cronconfig.Load(core.Config.ConfigDirectory,
		cronconfig.Options{
			MaxMessageBytes: core.Config.MaxMessageBytes,
			MaxSchemaBytes:  core.Config.MaxOutputSchemaBytes,
			ModelIDs:        models,
			TelegramEnabled: core.Config.Telegram,
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load cron jobs")
	}

	for index := range jobs {
		if jobs[index].TelegramChatID != 0 {
			policy, err := core.Config.TelegramPolicy()
			if err != nil {
				return nil, ctxerrors.Wrap(err, "validate cron Telegram policy")
			}

			_, allowed := policy.Chats[jobs[index].TelegramChatID]
			if !allowed {
				return nil, ctxerrors.Wrapf(peenconfig.ErrInvalidConfig,
					"cron job %q telegramChatId is not allowed",
					jobs[index].Name)
			}
		}

		// An omitted agentSpace stays empty, so the job follows its
		// workspace's agent space like every other door.
		if jobs[index].AgentSpace == "" {
			continue
		}

		if _, err := core.AgentSpaces.Select(
			jobs[index].AgentSpace); err != nil {
			return nil, ctxerrors.Wrapf(err,
				"cron job %q agentSpace", jobs[index].Name)
		}
	}

	return jobs, nil
}
