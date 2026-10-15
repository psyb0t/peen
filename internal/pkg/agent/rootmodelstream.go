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

func (m *rootWorkflowModelMonitor) closeInterruptedStream(
	ctx context.Context,
	prepared *preparedTurn,
	cause error,
) error {
	if cause == nil || !m.streamStarted || m.assistantClosed {
		return cause
	}

	// Closing the partial block advances the adapter before a retry starts.
	// The partial output stays in events, not as a completed assistant message.
	if err := prepared.adapter.OnAssistantMessage(context.WithoutCancel(ctx),
		elelem.Message{Role: elelem.RoleAssistant}); err != nil {
		return ctxerrors.Wrap(errors.Join(cause, err),
			"close interrupted workflow stream")
	}

	return cause
}
