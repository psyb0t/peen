package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// externalEventSource labels events published through the HTTP API, so a
// reader can tell caller-reported events from Peen's own producers.
const externalEventSource = "api"

// ListSessionNotices returns durable notice history without consuming it.
func (r *Runtime) ListSessionNotices(
	ctx context.Context,
	params api.ListSessionNoticesParams,
) (*api.SessionNoticePage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListSessionNotices(
		ctx,
		params.XSessionID,
		session.ListSessionNoticesOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable session notices")
	}

	pageLimit, pageOffset, err := durablePageValues(
		stored.Limit,
		stored.Offset,
		"notice",
	)
	if err != nil {
		return nil, err
	}

	page := api.SessionNoticePage{
		HasMore: stored.HasMore,
		Limit:   pageLimit,
		Notices: make([]api.SessionNotice, 0, len(stored.Items)),
		Offset:  pageOffset,
	}
	for _, notice := range stored.Items {
		converted, convertErr := sessionNoticeModelToAPI(notice)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Notices = append(page.Notices, converted)
	}

	return &page, nil
}

// EventLimits lets hook runners refuse an oversized event instead of having
// the bus cut it. It sits here rather than beside the type because the
// publisher is only ever built when the runtime has a bus.
func (p runtimeEventPublisher) EventLimits() events.Limits {
	return p.runtime.eventLimits()
}

// eventLimits reports the bounds events are held to, falling back to the
// defaults for a runtime with no bus, which rejects publishing anyway.
func (r *Runtime) eventLimits() events.Limits {
	if r.eventBus == nil {
		return events.DefaultLimits()
	}

	return r.eventBus.EventLimits()
}

// PublishSessionNotice records an outside report against a session. The
// reserved-prefix rule is applied here rather than on the bus, because Peen's
// own producers legitimately publish job and agent types and only an external
// caller must be stopped from forging them.
func (r *Runtime) PublishSessionNotice(
	ctx context.Context,
	sessionID uuid.UUID,
	request api.SessionNoticeRequest,
) (*api.SessionNotice, error) {
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

	// The bus cuts or drops oversized content to protect itself. An outside
	// caller gets told instead of receiving a 202 for a notice that lost it.
	if err := r.eventLimits().Validate(request.Summary, data); err != nil {
		return nil, ctxerrors.Wrap(err, "validate published event size")
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

	converted, err := sessionNoticeToAPI(published)
	if err != nil {
		return nil, err
	}

	return &converted, nil
}

func publishedDelivery(
	requested *api.SessionNoticeRequestDelivery,
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

func sessionNoticeToAPI(notice events.Notice) (api.SessionNotice, error) {
	converted := api.SessionNotice{
		Id:        notice.ID,
		Type:      notice.Type,
		Source:    notice.Source,
		Summary:   notice.Summary,
		Delivery:  api.SessionNoticeDelivery(notice.Delivery),
		CreatedAt: notice.CreatedAt,
	}

	if len(notice.Data) == 0 {
		return converted, nil
	}

	data := map[string]any{}
	if err := json.Unmarshal(notice.Data, &data); err != nil {
		return api.SessionNotice{}, ctxerrors.Wrap(
			err,
			"decode stored event data",
		)
	}

	converted.Data = &data

	return converted, nil
}

func sessionNoticeModelToAPI(
	notice *models.SessionNotice,
) (api.SessionNotice, error) {
	return sessionNoticeToAPI(events.Notice{
		ID:        notice.ID,
		SessionID: notice.SessionID,
		Type:      notice.Type,
		Source:    notice.Source,
		Summary:   notice.Summary,
		Data:      []byte(notice.DataJSON),
		Delivery:  string(notice.Delivery),
		CreatedAt: notice.CreatedAt,
	})
}
