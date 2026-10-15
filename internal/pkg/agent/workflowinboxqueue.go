package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) restoreRootWorkflowQueue(prepared *preparedTurn) error {
	if !prepared.workflowInbox {
		return nil
	}

	queue, err := newActiveUserMessageQueue(prepared, r.maxQueuedUserMessages)
	if err != nil {
		return ctxerrors.Wrap(err, "construct restored inbox queue")
	}

	prepared.userMessages = queue

	return nil
}

func (r *Runtime) bindRootWorkflowInbox(
	ctx context.Context,
	prepared *preparedTurn,
	executionID uuid.UUID,
) error {
	queue := prepared.userMessages
	if queue == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native root queue")
	}

	if err := queue.bindWorkflowInbox(ctx, executionID); err != nil {
		return ctxerrors.Wrap(err, "hydrate native root queue")
	}

	prepared.workflowInbox = true
	sessionID := prepared.lease.SessionID

	r.userMessageQueuesMutex.Lock()
	previous := r.userMessageQueues[sessionID]
	r.userMessageQueues[sessionID] = queue
	r.userMessageQueuesMutex.Unlock()

	if previous != nil && previous != queue {
		previous.close()
		previous.retire()
	}

	ctxscope.GetLogger(ctx).Info("workflow inbox queue bound",
		"session_id", sessionID, "execution_id", executionID,
		"turn_id", prepared.lease.TurnID)

	return nil
}

func (q *activeUserMessageQueue) bindWorkflowInbox(
	ctx context.Context,
	executionID uuid.UUID,
) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.closed || q.queue.Len() != 0 ||
		(q.executionID != uuid.Nil && q.executionID != executionID) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"native inbox cannot replace this queue")
	}

	store, ok := q.turn.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native queue requires execution storage")
	}

	execution, err := store.GetExecution(ctx, q.turn.lease.SessionID,
		executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read queue execution identity")
	}

	if execution.Kind != models.ExecutionKindRoot ||
		execution.RootTurnID != q.turn.lease.TurnID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"queue belongs to another execution turn")
	}

	pending, names, err := q.readPendingWorkflowInbox(ctx, store, executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "restore pending queue identities")
	}

	q.executionID = executionID
	q.workflowPending = pending
	q.skills.record(names...)

	return nil
}

func (q *activeUserMessageQueue) readPendingWorkflowInbox(
	ctx context.Context,
	store session.ExecutionStorage,
	executionID uuid.UUID,
) (map[uuid.UUID]int, []string, error) {
	pending := make(map[uuid.UUID]int)

	var names []string

	options := session.ListExecutionInboxOptions{
		State: models.InboxMessageStateQueued,
	}
	for {
		page, err := store.ListExecutionMessages(ctx, q.turn.lease.SessionID,
			executionID, options)
		if err != nil {
			return nil, nil, ctxerrors.Wrap(err, "read pending workflow queue")
		}

		for _, row := range page.Items {
			var parts []session.ExecutionInboxPart
			if err := json.Unmarshal([]byte(row.PartsJSON),
				&parts); err != nil {
				return nil, nil, ctxerrors.Wrap(err,
					"decode pending queue skills")
			}

			pending[row.RequestID] = 1 + len(parts)
			for _, part := range parts {
				if !part.SkillIsNote {
					names = append(names, part.SkillName)
				}
			}
		}

		if !page.HasMore {
			return pending, names, nil
		}

		options.Offset += len(page.Items)
	}
}

func (q *activeUserMessageQueue) markWorkflowInboxDelivered(
	requestIDs []uuid.UUID,
) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	for _, requestID := range requestIDs {
		delete(q.workflowPending, requestID)
	}
}
