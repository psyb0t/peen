// Package controlcron runs the controller's optional schedule dispatcher.
package controlcron

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
)

const ServiceName = control.CronServiceName

type ControlCron struct {
	handoff *control.Handoff
}

func New() (*ControlCron, error) {
	return &ControlCron{handoff: control.SharedHandoff()}, nil
}

func (*ControlCron) Name() string {
	return ServiceName
}

func (*ControlCron) Dependencies() []string {
	return []string{control.CoreServiceName}
}

func (s *ControlCron) Run(ctx context.Context) error {
	core, err := s.handoff.Await(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "await cron control core")
	}

	if !core.Config.Cron {
		<-ctx.Done()

		return nil
	}

	ctxscope.GetLogger(ctx).Info("cron scheduler started")

	if err := core.Cron.Run(ctx); err != nil {
		return ctxerrors.Wrap(err, "run cron scheduler")
	}

	return nil
}

func (*ControlCron) Stop(ctx context.Context) error {
	ctxscope.GetLogger(ctx).Info("cron scheduler stopping")

	return nil
}
