package control

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type conversationFeed struct {
	sessionID uuid.UUID
	requestID uuid.UUID
	cursor    int64
	pages     <-chan []session.FeedItem
	onEvent   func(session.FeedItem) error
}

func (c *Conversation) subscribe(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	onEvent func(session.FeedItem) error,
) (*conversationFeed, func(), error) {
	if onEvent == nil {
		return nil, func() {}, nil
	}

	head, err := c.registry.store.FeedHead(ctx, sessionID)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read conversation feed head")
	}

	pages, stop, err := c.feed.Subscribe(FeedFilter{
		SessionID: sessionID, RequestID: requestID,
	})
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "subscribe conversation feed")
	}

	return &conversationFeed{
		sessionID: sessionID, requestID: requestID,
		cursor: head, pages: pages, onEvent: onEvent,
	}, stop, nil
}

func (s *conversationFeed) observe(page []session.FeedItem) error {
	return s.observeUntil(page, math.MaxInt64)
}

func (s *conversationFeed) observeUntil(
	page []session.FeedItem,
	head int64,
) error {
	for _, item := range page {
		if item.Sequence > head {
			break
		}

		if item.Sequence <= s.cursor {
			continue
		}

		s.cursor = item.Sequence
		if item.SessionID != s.sessionID || item.RequestID != s.requestID ||
			item.Kind != session.FeedKindEvent {
			continue
		}

		if err := s.onEvent(item); err != nil {
			return ctxerrors.Wrap(err, "observe conversation event")
		}
	}

	return nil
}

// The worker can answer before the pump catches up. Read the committed tail
// once more, deduplicating rows already observed, before returning its answer.
func (c *Conversation) drainFeed(
	ctx context.Context,
	stream *conversationFeed,
) error {
	head, err := c.registry.store.FeedHead(ctx, stream.sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read completed conversation feed head")
	}

	for stream.cursor < head {
		page, err := c.registry.store.ListFeed(ctx, stream.sessionID,
			stream.cursor, session.MaximumFeedLimit)
		if err != nil {
			return ctxerrors.Wrap(err, "drain conversation feed")
		}

		if page.NextCursor <= stream.cursor {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"conversation feed did not advance")
		}

		if err := stream.observeUntil(page.Items, head); err != nil {
			return err
		}

		stream.cursor = min(page.NextCursor, head)
	}

	return nil
}
