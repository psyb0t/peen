package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// externalEventSource labels events published through the HTTP API, so a
// reader can tell caller-reported events from Peen's own producers.
const externalEventSource = "api"

// ListSessionEvents reports what is waiting for a session without consuming
// it. Reading over HTTP must not steal events the agent has not seen yet, so
// this peeks rather than drains.
func (r *Runtime) ListSessionEvents(
	ctx context.Context,
	sessionID uuid.UUID,
) (*api.SessionEventPage, error) {
	if r.eventBus == nil {
		return nil, ctxerrors.Wrap(
			ErrEventsUnavailable,
			"no event bus is configured",
		)
	}

	if _, err := r.store.Get(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "resolve event session")
	}

	batch := r.eventBus.Peek(sessionID)

	page := api.SessionEventPage{
		Events:  make([]api.SessionEvent, 0, len(batch.Notices)),
		Dropped: int32(batch.Dropped), //nolint:gosec // Bounded queue count.
	}

	for _, notice := range batch.Notices {
		converted, err := sessionEventToAPI(notice)
		if err != nil {
			return nil, err
		}

		page.Events = append(page.Events, converted)
	}

	return &page, nil
}

// PublishSessionEvent records an outside report against a session. The
// reserved-prefix rule is applied here rather than on the bus, because Peen's
// own producers legitimately publish job and agent types and only an external
// caller must be stopped from forging them.
func (r *Runtime) PublishSessionEvent(
	ctx context.Context,
	sessionID uuid.UUID,
	request api.SessionEventRequest,
) (*api.SessionEvent, error) {
	if err := events.ValidateExternalType(request.Type); err != nil {
		return nil, ctxerrors.Wrap(err, "validate published event type")
	}

	delivery, err := publishedDelivery(request.Delivery)
	if err != nil {
		return nil, err
	}

	data, err := publishedData(request.Data)
	if err != nil {
		return nil, err
	}

	published, err := r.PublishEvent(ctx, events.Notice{
		SessionID: sessionID,
		Type:      request.Type,
		Source:    externalEventSource,
		Summary:   request.Summary,
		Data:      data,
		Delivery:  delivery,
	})
	if err != nil {
		return nil, err
	}

	converted, err := sessionEventToAPI(published)
	if err != nil {
		return nil, err
	}

	return &converted, nil
}

func publishedDelivery(
	requested *api.SessionEventRequestDelivery,
) (events.Delivery, error) {
	value := events.DeliveryQueue
	if requested != nil {
		value = string(*requested)
	}

	delivery, err := events.ValidateDelivery(value)
	if err != nil {
		return "", ctxerrors.Wrap(err, "validate published event delivery")
	}

	return delivery, nil
}

func publishedData(data *map[string]any) (json.RawMessage, error) {
	if data == nil || len(*data) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(*data)
	if err != nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"published event data must be JSON encodable",
		)
	}

	return encoded, nil
}

func sessionEventToAPI(notice events.Notice) (api.SessionEvent, error) {
	converted := api.SessionEvent{
		Id:        notice.ID,
		Type:      notice.Type,
		Source:    notice.Source,
		Summary:   notice.Summary,
		Delivery:  api.SessionEventDelivery(notice.Delivery),
		CreatedAt: notice.CreatedAt,
	}

	if len(notice.Data) == 0 {
		return converted, nil
	}

	data := map[string]any{}
	if err := json.Unmarshal(notice.Data, &data); err != nil {
		return api.SessionEvent{}, ctxerrors.Wrap(
			err,
			"decode stored event data",
		)
	}

	converted.Data = &data

	return converted, nil
}
