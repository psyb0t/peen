package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) ListDeferredExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListEventsOptions,
) (*session.ExecutionInboxPage, error) {
	return executionCall[*session.ExecutionInboxPage](ctx, s, sessionID,
		executionRequest{
			Operation: executionListDeferredMessages, Page: options,
		})
}

func (c *Controller) callExecutionDeferred(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	//nolint:exhaustive // Other operations have their own dispatcher.
	switch r.Operation {
	case executionDeferMessage:
		return c.deferExecutionMessage(ctx, r)
	case executionAcquireDeferredTurn:
		return c.acquireDeferredTurn(ctx, r)
	case executionListDeferredMessages:
		page, err := c.store.ListDeferredExecutionMessages(ctx, c.sessionID,
			r.Page)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read deferred worker inputs")
		}

		return page, nil
	default:
		return c.callExecutionInbox(ctx, r)
	}
}

func (s *Store) DeferExecutionMessage(
	ctx context.Context,
	executionID uuid.UUID,
	lease session.Lease,
	input session.AcceptExecutionMessageInput,
) (*session.ExecutionInboxAcceptance, error) {
	return executionCall[*session.ExecutionInboxAcceptance](ctx, s,
		lease.SessionID, executionRequest{
			Operation: executionDeferMessage, ExecutionID: executionID,
			TurnLease: lease, AcceptMessage: input,
		})
}

func (c *Controller) deferExecutionMessage(
	ctx context.Context,
	r executionRequest,
) (*session.ExecutionInboxAcceptance, error) {
	if err := c.assertLease(r.TurnLease); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize deferred inbox turn")
	}

	result, err := c.store.DeferExecutionMessage(ctx, r.ExecutionID,
		r.TurnLease, r.AcceptMessage)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "defer worker inbox message")
	}

	return result, nil
}
