package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (n *nativeWorkflowRunner) scheduleDeferredRoot(
	ctx context.Context,
) error {
	runtime := n.dispatch.runtime

	stored, err := runtime.store.Get(ctx, runtime.sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred scheduling revision")
	}

	options := session.ListEventsOptions{}
	for {
		page, err := n.engine.options.Store.ListDeferredExecutionMessages(ctx,
			runtime.sessionID, options)
		if err != nil {
			return ctxerrors.Wrap(err, "read deferred scheduling input")
		}

		for _, message := range page.Items {
			if message.StopRevision != stored.StopRevision {
				ctxscope.GetLogger(ctx).Debug("deferred input remains stopped",
					"inbox_id", message.ID, "reason", "older_stop_revision")

				continue
			}

			err := n.submitDeferredRoot(ctx, message)
			if errors.Is(err, session.ErrSessionBusy) {
				return nil
			}

			if errors.Is(err, commerr.ErrInvalidState) {
				ctxscope.GetLogger(ctx).Warn("deferred input retained",
					"inbox_id", message.ID,
					"reason", "admission_unavailable", "err", err)

				continue
			}

			if err != nil {
				return ctxerrors.Wrap(err, "submit deferred input")
			}

			return nil
		}

		if !page.HasMore {
			return nil
		}

		options.Offset += len(page.Items)
	}
}

func (n *nativeWorkflowRunner) submitDeferredRoot(
	ctx context.Context,
	message *models.ExecutionInboxMessage,
) error {
	runtime := n.dispatch.runtime

	saved, err := runtime.loadDeferredRoot(ctx, message.ExecutionID,
		message.RequestID, nil)
	if err != nil {
		return ctxerrors.Wrap(err, "load deferred scheduling input")
	}

	ctx = context.WithValue(ctx, deferredRootContextKey{}, saved)

	ctx, admission, err := runtime.admitRequest(ctx, saved.request)
	if err != nil {
		return ctxerrors.Wrap(err, "reserve deferred admission")
	}
	defer admission.close()

	if runtime.store.IsActive(runtime.sessionID) {
		return nil
	}

	prepared, err := runtime.prepareTurn(ctx, admission.input)
	if err != nil {
		return ctxerrors.Wrap(err, "prepare deferred submission")
	}

	prepared.deferredInboxID = &saved.inboxID
	prepared.attachPublisher(context.WithoutCancel(ctx))

	if err := runtime.registerActiveUserMessageQueue(prepared); err != nil {
		return n.failOpening(ctx, prepared, err)
	}

	if err := runtime.startLease(ctx, prepared); err != nil {
		return n.failOpening(ctx, prepared, err)
	}

	submission, err := n.dispatch.submitRoot(ctx, n.api, prepared)
	if err != nil {
		if submission.Input.ExecutionID == uuid.Nil {
			return n.failOpening(ctx, prepared, err)
		}

		return ctxerrors.Wrap(errors.Join(err,
			n.cleanupFailedSubmission(ctx, prepared, submission.Input)),
			"retain failed deferred submission")
	}

	ctxscope.GetLogger(ctx).Info("deferred root submitted",
		"inbox_id", message.ID, "execution_id", submission.Input.ExecutionID)

	return nil
}
