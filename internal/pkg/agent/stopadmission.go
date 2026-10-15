package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type turnAdmission struct {
	input  TurnRequest
	ticket *session.AdmissionTicket
	cancel context.CancelFunc
}

func (a *turnAdmission) close() {
	a.ticket.Release()
	a.cancel()
}

func (r *Runtime) admitRequest(
	ctx context.Context,
	input TurnRequest,
) (context.Context, *turnAdmission, error) {
	ticket := r.admissions.Reserve(r.turnSessionID(input))
	bound, cancel := ticket.BindContext(ctx)

	admitted := &turnAdmission{
		input: input, ticket: ticket,
		cancel: cancel,
	}
	if err := r.captureStopRevision(bound, &admitted.input); err != nil {
		admitted.close()

		return nil, nil, ctxerrors.Wrap(err, "capture request stop revision")
	}

	if err := ticket.Wait(bound); err != nil {
		admitted.close()

		return nil, nil, ctxerrors.Wrap(err, "admit turn request")
	}

	return bound, admitted, nil
}

func (r *Runtime) captureStopRevision(
	ctx context.Context,
	input *TurnRequest,
) error {
	if deferredRootFromContext(ctx) != nil {
		return nil
	}

	sessionID := r.turnSessionID(*input)
	if sessionID == uuid.Nil {
		return nil
	}

	stored, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read request stop revision")
	}

	if err := session.ValidateStopRevision(input.ExpectedStopRevision,
		stored.StopRevision); err != nil {
		return ctxerrors.Wrap(err, "validate request stop revision")
	}

	revision := stored.StopRevision
	input.ExpectedStopRevision = &revision

	return nil
}
