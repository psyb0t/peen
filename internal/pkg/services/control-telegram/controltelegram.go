// Package controltelegram runs the controller's optional Telegram polling.
package controltelegram

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
)

const ServiceName = control.TelegramServiceName

type ControlTelegram struct {
	handoff *control.Handoff
}

func New() (*ControlTelegram, error) {
	return &ControlTelegram{handoff: control.SharedHandoff()}, nil
}

func (*ControlTelegram) Name() string {
	return ServiceName
}

func (*ControlTelegram) Dependencies() []string {
	return []string{control.CoreServiceName}
}

func (s *ControlTelegram) Run(ctx context.Context) error {
	core, err := s.handoff.Await(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "await Telegram control core")
	}

	if !core.Config.Telegram {
		<-ctx.Done()

		return nil
	}

	ctxscope.GetLogger(ctx).Info("Telegram polling started")

	if err := core.Telegram.Run(ctx); err != nil {
		return ctxerrors.Wrap(err, "run Telegram controller")
	}

	return nil
}

func (*ControlTelegram) Stop(ctx context.Context) error {
	ctxscope.GetLogger(ctx).Info("Telegram polling stopping")

	return nil
}
