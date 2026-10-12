package peen

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// NoticeRequest publishes external data. Reserved job and agent types are
// refused; wake delivery still requires a matching event handler.
type NoticeRequest struct {
	Type     string
	Summary  string
	Data     map[string]any
	Delivery Delivery
}

// PublishEvent stores a report and wakes an idle session when its handler asks.
func (s *Session) PublishEvent(
	ctx context.Context, request NoticeRequest,
) (SessionNotice, error) {
	return sessionRead[SessionNotice](ctx, s,
		func(ctx context.Context) (any, error) {
			input := api.SessionNoticeRequest{
				Type:    request.Type,
				Summary: request.Summary,
				Data:    &request.Data,
			}
			if request.Delivery != "" {
				delivery := api.SessionNoticeRequestDelivery(request.Delivery)
				input.Delivery = &delivery
			}

			return s.runtime.internal.PublishSessionNotice(ctx, s.id, input)
		})
}
