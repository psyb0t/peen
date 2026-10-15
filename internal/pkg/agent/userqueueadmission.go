package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const stoppedQueueAdmissionReason = "older_queue_stop_revision"

func (r *Runtime) admitUserMessageQueue(
	ctx context.Context,
	sessionID uuid.UUID,
	input TurnRequest,
) (*activeUserMessageQueue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, ctxerrors.Wrap(err, "wait for stopped turn cleanup")
		}

		r.userMessageQueuesMutex.Lock()
		queue := r.userMessageQueues[sessionID]
		r.userMessageQueuesMutex.Unlock()

		if queue == nil || input.ExpectedStopRevision == nil ||
			queue.stopRevision == *input.ExpectedStopRevision {
			return queue, nil
		}

		ctxscope.GetLogger(ctx).Debug("waiting for stopped turn cleanup",
			"session_id", sessionID, "turn_id", queue.turn.lease.TurnID,
			"reason", stoppedQueueAdmissionReason)

		select {
		case <-queue.retired:
		case <-ctx.Done():
			return nil, ctxerrors.Wrap(ctx.Err(), "stop queue admission wait")
		}
	}
}

func (q *activeUserMessageQueue) retire() {
	q.retireOnce.Do(func() { close(q.retired) })
}
