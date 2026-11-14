package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

type rootWorkflowModelMonitor struct {
	startedAt       time.Time
	firstDeltaAt    time.Time
	firstDeltaOnce  sync.Once
	streamStarted   bool
	assistantClosed bool
}

func (m *rootWorkflowModelMonitor) bind(
	request *elelem.Request,
	prepared, assistant *preparedTurn,
	audit *modelAuditRecorder,
	round int,
) *elelem.Request {
	request.OnDelta(func(context.Context, elelem.Delta) error {
		m.firstDeltaOnce.Do(func() { m.firstDeltaAt = time.Now() })

		return nil
	})
	request = prepared.bindRootModelCallbacksWithAssistant(request, audit,
		assistant).
		OnRoundStart(func(context.Context, *elelem.RoundEvent) error {
			m.streamStarted = true

			return nil
		}).OnAssistantMessage(func(context.Context, elelem.Message) error {
		m.assistantClosed = true

		return nil
	}).OnRoundEnd(func(ctx context.Context, event *elelem.RoundEvent) error {
		logicalRound := *event
		logicalRound.Round = round

		return prepared.adapter.OnRoundEnd(ctx, &logicalRound)
	})

	return request
}

func (m *rootWorkflowModelMonitor) runRequest(
	ctx context.Context,
	request *elelem.Request,
	prepared *preparedTurn,
	audit *modelAuditRecorder,
	timeout time.Duration,
) (*elelem.Response, error) {
	return runProviderReplay(ctx, request, timeout,
		prepared.rootRetryCallback(audit), func(ctx context.Context) error {
			return m.closePartialStream(ctx, prepared)
		})
}

func (m *rootWorkflowModelMonitor) closeInterruptedStream(
	ctx context.Context,
	prepared *preparedTurn,
	cause error,
) error {
	if cause == nil {
		return nil
	}

	if err := m.closePartialStream(ctx, prepared); err != nil {
		return ctxerrors.Wrap(errors.Join(cause, err),
			"close interrupted workflow stream")
	}

	return cause
}

func (m *rootWorkflowModelMonitor) closePartialStream(
	ctx context.Context,
	prepared *preparedTurn,
) error {
	if !m.streamStarted || m.assistantClosed {
		return nil
	}

	// Advance the adapter without promoting partial output to a message.
	if err := prepared.adapter.OnAssistantMessage(context.WithoutCancel(ctx),
		elelem.Message{Role: elelem.RoleAssistant}); err != nil {
		return ctxerrors.Wrap(err, "close partial workflow stream")
	}

	m.streamStarted = false

	return nil
}
