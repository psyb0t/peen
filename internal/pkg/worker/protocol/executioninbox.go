package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) AcceptChildExecutionMessage(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input session.AcceptExecutionMessageInput,
) (*session.ExecutionInboxAcceptance, error) {
	return executionCall[*session.ExecutionInboxAcceptance](ctx, s, sessionID,
		executionRequest{
			Operation:   executionAcceptChildMessage,
			ExecutionID: executionID, AcceptMessage: input,
		})
}

var _ session.ChildExecutionInboxStorage = (*Store)(nil)

func (s *Store) DeliverChildExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	requestIDs []uuid.UUID,
) (*session.ExecutionInboxDelivery, error) {
	return executionCall[*session.ExecutionInboxDelivery](ctx, s, sessionID,
		executionRequest{
			Operation: executionDeliverChildMessages,
			DeliverMessages: session.DeliverExecutionMessagesInput{
				ModelLease: lease, RequestIDs: requestIDs,
			},
		})
}

func (s *Store) GetExecutionMessageByRequest(
	ctx context.Context,
	sessionID, executionID, requestID uuid.UUID,
) (*models.ExecutionInboxMessage, error) {
	return executionCall[*models.ExecutionInboxMessage](ctx, s, sessionID,
		executionRequest{
			Operation:   executionGetMessageByRequest,
			ExecutionID: executionID, RequestID: requestID,
		})
}

func (s *Store) AcceptExecutionMessage(
	ctx context.Context,
	executionID uuid.UUID,
	lease session.Lease,
	input session.AcceptExecutionMessageInput,
) (*session.ExecutionInboxAcceptance, error) {
	return executionCall[*session.ExecutionInboxAcceptance](ctx, s,
		lease.SessionID, executionRequest{
			Operation: executionAcceptMessage, ExecutionID: executionID,
			TurnLease: lease, AcceptMessage: input,
		})
}

func (s *Store) ListExecutionMessages(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options session.ListExecutionInboxOptions,
) (*session.ExecutionInboxPage, error) {
	return executionCall[*session.ExecutionInboxPage](ctx, s, sessionID,
		executionRequest{
			Operation:   executionListMessages,
			ExecutionID: executionID, InboxPage: options,
		})
}

func (s *Store) DeliverExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.DeliverExecutionMessagesInput,
) (*session.ExecutionInboxDelivery, error) {
	return executionCall[*session.ExecutionInboxDelivery](ctx, s, sessionID,
		executionRequest{
			Operation:       executionDeliverMessages,
			DeliverMessages: input,
		})
}

func (c *Controller) callExecutionInbox(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	//nolint:exhaustive // Other execution operations have their own dispatch.
	switch r.Operation {
	case executionGetMessageByRequest:
		message, err := c.store.GetExecutionMessageByRequest(ctx, c.sessionID,
			r.ExecutionID, r.RequestID)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read worker inbox request")
		}

		return message, nil
	case executionListMessages:
		page, err := c.store.ListExecutionMessages(ctx, c.sessionID,
			r.ExecutionID, r.InboxPage)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "list worker inbox messages")
		}

		return page, nil
	case executionAcceptMessage:
		return c.acceptExecutionMessage(ctx, r)
	case executionAcceptChildMessage:
		return c.acceptChildExecutionMessage(ctx, r)
	case executionDeliverMessages:
		return c.deliverExecutionMessages(ctx, r.DeliverMessages)
	case executionDeliverChildMessages:
		return c.deliverChildExecutionMessages(ctx, r.DeliverMessages)
	default:
		return nil, ctxerrors.Wrap(commerr.ErrNotImplemented,
			"execution store operation is not published")
	}
}

func (c *Controller) acceptChildExecutionMessage(
	ctx context.Context,
	r executionRequest,
) (*session.ExecutionInboxAcceptance, error) {
	accepted, err := c.store.AcceptChildExecutionMessage(ctx, c.sessionID,
		r.ExecutionID, r.AcceptMessage)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "accept worker child inbox message")
	}

	return accepted, nil
}

func (c *Controller) deliverChildExecutionMessages(
	ctx context.Context,
	input session.DeliverExecutionMessagesInput,
) (*session.ExecutionInboxDelivery, error) {
	if input.ModelLease.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"child model activity differs from its connection")
	}

	receipt, err := c.store.DeliverChildExecutionMessages(ctx, c.sessionID,
		input.ModelLease, input.RequestIDs)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "deliver worker child inbox messages")
	}

	return receipt, nil
}

func (c *Controller) acceptExecutionMessage(
	ctx context.Context,
	r executionRequest,
) (*session.ExecutionInboxAcceptance, error) {
	if err := c.assertLease(r.TurnLease); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize inbox acceptance turn")
	}

	result, err := c.store.AcceptExecutionMessage(ctx, r.ExecutionID,
		r.TurnLease, r.AcceptMessage)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "accept worker inbox message")
	}

	return result, nil
}

func (c *Controller) deliverExecutionMessages(
	ctx context.Context,
	input session.DeliverExecutionMessagesInput,
) (*session.ExecutionInboxDelivery, error) {
	if err := c.assertLease(input.TurnLease); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize inbox delivery turn")
	}

	if input.ModelLease.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"inbox model activity differs from its connection")
	}

	receipt, err := c.store.DeliverExecutionMessages(ctx, c.sessionID, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "deliver worker inbox messages")
	}

	return receipt, nil
}
