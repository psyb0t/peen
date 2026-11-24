package control

import (
	"slices"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// FeedFilter selects one session and, when set, one root request in it.
type FeedFilter struct {
	SessionID uuid.UUID
	RequestID uuid.UUID
}

type feedSubscription struct {
	filter FeedFilter
	pages  chan []session.FeedItem
}

// Subscribe observes future committed rows. A full buffer disconnects the
// subscriber by closing its channel; delivery never waits for its reader.
func (p *FeedPump) Subscribe(
	filter FeedFilter,
) (<-chan []session.FeedItem, func(), error) {
	if filter.SessionID == uuid.Nil {
		return nil, nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"feed subscription requires a session")
	}

	subscriber := &feedSubscription{
		filter: filter,
		pages:  make(chan []session.FeedItem, feedSubscriberBuffer),
	}

	p.subscriberMutex.Lock()
	defer p.subscriberMutex.Unlock()

	if err := p.ctx.Err(); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "subscribe to stopped feed")
	}

	p.subscribers[subscriber] = struct{}{}

	return subscriber.pages, func() { p.removeSubscriber(subscriber) }, nil
}

func (p *FeedPump) removeSubscriber(subscriber *feedSubscription) {
	p.subscriberMutex.Lock()
	defer p.subscriberMutex.Unlock()

	if _, exists := p.subscribers[subscriber]; exists {
		delete(p.subscribers, subscriber)
		close(subscriber.pages)
	}
}

func (p *FeedPump) closeSubscribers() {
	p.subscriberMutex.Lock()
	defer p.subscriberMutex.Unlock()

	for subscriber := range p.subscribers {
		close(subscriber.pages)
		delete(p.subscribers, subscriber)
	}
}

func (p *FeedPump) deliverSubscribers(
	sessionID uuid.UUID,
	items []session.FeedItem,
) {
	p.subscriberMutex.Lock()
	defer p.subscriberMutex.Unlock()

	for subscriber := range p.subscribers {
		if subscriber.filter.SessionID != sessionID {
			continue
		}

		page := slices.DeleteFunc(slices.Clone(items),
			func(item session.FeedItem) bool {
				return subscriber.filter.RequestID != uuid.Nil &&
					item.RequestID != subscriber.filter.RequestID
			})
		if len(page) == 0 {
			continue
		}

		select {
		case subscriber.pages <- page:
		default:
			close(subscriber.pages)
			delete(p.subscribers, subscriber)
			ctxscope.GetLogger(p.ctx).Warn("feed subscriber disconnected",
				"reason", "subscriber_buffer_full", "session_id", sessionID,
				"request_id", subscriber.filter.RequestID)
		}
	}
}
