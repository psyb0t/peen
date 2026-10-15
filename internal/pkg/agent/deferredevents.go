package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func rootStartEvents(
	ctx context.Context,
	input TurnRequest,
	requestID uuid.UUID,
	skills []skillMessage,
) ([]Event, []session.EventInput, error) {
	events, records, err := turnStartEvents(input, requestID, skills)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "build root admission events")
	}

	if deferredRootFromContext(ctx) != nil {
		events[0].Type = EventTypeUserMessageDelivered
		records[0].EventType = EventTypeUserMessageDelivered
	}

	return events, records, nil
}
