package agent

import (
	"context"
	"errors"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

func (n *nativeWorkflowRunner) cleanupFailedSubmission(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowExecutionInput,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		workflowPauseTimeout)
	defer cancel()

	slot := n.dispatch.root.slot(input.ExecutionID)

	release, err := slot.gate.acquire(cleanup, true)
	if err != nil {
		return ctxerrors.Wrap(err,
			"admit unsubmitted root cleanup")
	}
	defer release()

	shared := slot.current
	if shared != nil {
		drain, err := shared.gate.acquire(cleanup, true)
		if err != nil {
			return ctxerrors.Wrap(err,
				"drain unsubmitted root callbacks")
		}
		defer drain()
	}

	_, err = n.api.GetWorkflowInstanceState(cleanup,
		nativeWorkflowInstance(input.SessionID, input.ExecutionID))
	if err == nil {
		return nil
	}

	if !errors.Is(err, backend.ErrInstanceNotFound) {
		return ctxerrors.Wrap(err, "verify failed root submission state")
	}

	if shared != nil && (shared.prepared != prepared ||
		shared.lease != prepared.lease) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"unsubmitted root holder changed")
	}

	n.dispatch.runtime.store.ReleaseTurn(prepared.lease)
	n.dispatch.runtime.closeActiveUserMessageQueue(prepared)
	prepared.turn.closeChildSink()

	if shared == nil {
		prepared.releaseTurnCapacity()
	} else {
		shared.releaseTurnCapacity()
	}

	slot.current = nil

	ctxscope.GetLogger(cleanup).Warn("unscheduled root holder released",
		"execution_id", input.ExecutionID, "reason", "sdk_instance_missing")

	return nil
}

func (p *preparedTurn) releaseTurnCapacity() {
	if p.releaseTurnSlot != nil {
		p.releaseTurnSlot()
	}
}
