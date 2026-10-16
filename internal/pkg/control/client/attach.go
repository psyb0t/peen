package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

const (
	webSocketPath        = "/ws/v1"
	webSocketScheme      = "ws"
	webSocketSubprotocol = "peen.v1"
	// webSocketBearerPrefix is a public protocol label, not a credential.
	//nolint:gosec // The token follows this prefix, the prefix is not secret.
	webSocketBearerPrefix      = "peen.bearer."
	webSocketSessionIDQueryKey = "sessionId"

	// webSocketProtocolCapacity covers the subprotocol plus an optional bearer.
	webSocketProtocolCapacity = 2

	// attachMetadataSequence is the live frame metadata key naming a frame's
	// position in the session feed. Its absence marks an unsequenced live-only
	// acknowledgement such as message.completed or message.failed.
	attachMetadataSequence = "sequence"

	// attachReconnectInitialBackoff is the wait before the first reconnect
	// attempt after an unexpected close.
	attachReconnectInitialBackoff = 500 * time.Millisecond

	// attachReconnectMaxBackoff caps the doubling backoff between reconnects.
	attachReconnectMaxBackoff = 10 * time.Second

	// attachReconnectJitterDivisor bounds the random jitter added to each
	// wait to at most half the current backoff, so many attached clients
	// reconnecting at once do not all retry on the same tick.
	attachReconnectJitterDivisor = 2

	// attachReconnectBackoffMultiplier doubles the wait on every retry.
	attachReconnectBackoffMultiplier = 2
)

// AttachEvent is one event delivered to an attached client, live or replayed
// from the session feed during catch-up.
type AttachEvent struct {
	Type string
	Data []byte

	// Sequence is the frame's position in the session feed. It is nil for an
	// unsequenced live-only acknowledgement, which is never replayed.
	Sequence *int64
}

// attachOutcome is what ended one dialed socket's stream.
type attachOutcome int

const (
	// attachOutcomeStopped means the context ended or the server closed the
	// socket normally: Attach returns without reconnecting.
	attachOutcomeStopped attachOutcome = iota

	// attachOutcomeReconnect means the socket ended unexpectedly: Attach backs
	// off, catches up over REST, and dials again.
	attachOutcomeReconnect
)

// Attach streams the controller's live feed for one session until the context
// ends or the controller closes the socket normally.
//
// The filter is server side: the socket asks for one session's events rather
// than receiving every session's and discarding most.
//
// cursor, when non-nil, replays the feed from that sequence before joining
// live. A nil cursor starts live at the session's current feed head, read
// first through GET /api/v1/session, so a later reconnect has a defined
// place to resume from. An unexpected close or read error reconnects with a
// doubling backoff and catches up every frame missed over REST before
// continuing live; a gap in the live stream itself (sequence > last+1)
// catches up the same way without a reconnect. A duplicate (sequence <=
// last) is dropped.
func (c *Client) Attach(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor *int64,
	sink func(AttachEvent) error,
) error {
	if sessionID == uuid.Nil {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"attach requires a session ID",
		)
	}

	last, err := c.attachStartingSequence(ctx, sessionID, cursor, sink)
	if err != nil {
		if ctx.Err() != nil {
			//nolint:nilerr // Cancelled before the first read: a clean stop.
			return nil
		}

		return err
	}

	backoff := attachReconnectInitialBackoff

	for {
		outcome, err := c.attachSession(ctx, sessionID, &last, sink)
		if err != nil {
			return err
		}

		if outcome == attachOutcomeStopped {
			return nil
		}

		if err := attachWaitBackoff(ctx, backoff); err != nil {
			//nolint:nilerr // Cancellation during backoff is a clean stop.
			return nil
		}

		backoff = nextAttachBackoff(backoff)

		if err := c.attachCatchUp(ctx, sessionID, &last, sink); err != nil {
			return err
		}
	}
}

// attachStartingSequence resolves where live streaming begins. A caller-given
// cursor replays from there first; otherwise it reads the session's current
// feed head and starts live from it with no replay.
func (c *Client) attachStartingSequence(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor *int64,
	sink func(AttachEvent) error,
) (int64, error) {
	if cursor != nil {
		last := *cursor
		if err := c.attachCatchUp(ctx, sessionID, &last, sink); err != nil {
			return 0, err
		}

		return last, nil
	}

	current, err := c.GetSession(ctx, sessionID)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read the session before attaching")
	}

	return current.FeedSequence, nil
}

// attachCatchUp replays the feed after last until the server reports no more,
// advancing last as it goes.
func (c *Client) attachCatchUp(
	ctx context.Context,
	sessionID uuid.UUID,
	last *int64,
	sink func(AttachEvent) error,
) error {
	for {
		page, err := c.ListSessionFeed(ctx, sessionID, *last)
		if err != nil {
			return ctxerrors.Wrap(err, "catch up the session feed")
		}

		for _, frame := range page.Items {
			event, err := attachEventFromFrame(frame)
			if err != nil {
				return err
			}

			if err := sink(event); err != nil {
				return ctxerrors.Wrap(
					err,
					"deliver a replayed session feed frame",
				)
			}
		}

		*last = page.NextCursor

		if !page.HasMore {
			return nil
		}
	}
}

// attachSession dials one socket and streams it until it ends.
func (c *Client) attachSession(
	ctx context.Context,
	sessionID uuid.UUID,
	last *int64,
	sink func(AttachEvent) error,
) (attachOutcome, error) {
	connection, err := c.dialWebSocket(ctx, sessionID)
	if err != nil {
		if ctx.Err() != nil {
			return attachOutcomeStopped, nil
		}

		return attachOutcomeReconnect, nil
	}

	// ReadJSON takes no context, so closing the socket is what unblocks it.
	// Once keeps the cancel path and the return path from double closing.
	var closeOnce sync.Once

	closeConnection := func() {
		closeOnce.Do(func() {
			if closeErr := connection.Close(); closeErr != nil {
				ctxscope.GetLogger(ctx).Debug(
					"closing the control socket failed",
					"err", closeErr,
				)
			}
		})
	}

	defer closeConnection()

	attachCtx, cancelAttach := context.WithCancel(ctx)
	defer cancelAttach()

	go func() {
		<-attachCtx.Done()
		closeConnection()
	}()

	return c.streamAttachedEvents(ctx, sessionID, connection, last, sink)
}

// streamAttachedEvents reads one socket until it closes, the context ends, or
// the sink fails, applying the duplicate/gap/ack rules to every live frame.
func (c *Client) streamAttachedEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	connection *websocket.Conn,
	last *int64,
	sink func(AttachEvent) error,
) (attachOutcome, error) {
	for {
		event := dabluveees.Event{}
		if err := connection.ReadJSON(&event); err != nil {
			if ctx.Err() != nil {
				//nolint:nilerr // Cancellation closed the read; a clean stop.
				return attachOutcomeStopped, nil
			}

			if websocket.IsCloseError(
				err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
			) {
				return attachOutcomeStopped, nil
			}

			return attachOutcomeReconnect, nil
		}

		sequence := attachSequence(event)

		if sequence != nil {
			for *sequence > *last+1 {
				err := c.attachCatchUp(ctx, sessionID, last, sink)
				if err != nil {
					return attachOutcomeStopped, err
				}
			}

			if *sequence <= *last {
				// A duplicate: already applied through catch-up or an
				// earlier frame.
				continue
			}

			*last = *sequence
		}

		if err := sink(AttachEvent{
			Type:     string(event.Type),
			Data:     event.Data,
			Sequence: sequence,
		}); err != nil {
			return attachOutcomeStopped, ctxerrors.Wrap(
				err,
				"deliver an attached session event",
			)
		}
	}
}

// attachSequence reads metadata.sequence off a live frame. JSON numbers decode
// through the generic metadata map as float64, so that is the case this
// checks for; anything else, including no sequence at all, is unsequenced.
func attachSequence(event dabluveees.Event) *int64 {
	if event.Metadata == nil {
		return nil
	}

	raw, found := event.Metadata.Get(attachMetadataSequence)
	if !found {
		return nil
	}

	value, ok := raw.(float64)
	if !ok {
		return nil
	}

	sequence := int64(value)

	return &sequence
}

// attachEventFromFrame converts one replayed session.feed page item to the
// same shape a live frame delivers.
func attachEventFromFrame(frame api.FeedFrame) (AttachEvent, error) {
	data, err := json.Marshal(frame.Data)
	if err != nil {
		return AttachEvent{}, ctxerrors.Wrap(
			err,
			"encode a replayed session feed frame",
		)
	}

	sequence := frame.Sequence

	return AttachEvent{Type: frame.Type, Data: data, Sequence: &sequence}, nil
}

// attachWaitBackoff waits backoff plus jitter, or returns early if ctx ends.
func attachWaitBackoff(ctx context.Context, backoff time.Duration) error {
	jitter := attachJitter(backoff / attachReconnectJitterDivisor)

	timer := time.NewTimer(backoff + jitter)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "wait out the reconnect backoff")
	}
}

// attachJitter returns a random duration in [0, limit). It spreads reconnects
// from many clients apart; a failed random read only loses that spread, so it
// falls back to no jitter rather than failing the reconnect.
func attachJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}

	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0
	}

	return time.Duration(value.Int64())
}

// nextAttachBackoff doubles the wait, capped at attachReconnectMaxBackoff.
func nextAttachBackoff(current time.Duration) time.Duration {
	next := current * attachReconnectBackoffMultiplier
	if next > attachReconnectMaxBackoff {
		return attachReconnectMaxBackoff
	}

	return next
}

func (c *Client) dialWebSocket(
	ctx context.Context,
	sessionID uuid.UUID,
) (*websocket.Conn, error) {
	endpoint, err := url.Parse(c.baseURL + webSocketPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the control socket URL")
	}

	endpoint.Scheme = webSocketScheme

	query := endpoint.Query()
	query.Set(webSocketSessionIDQueryKey, sessionID.String())
	endpoint.RawQuery = query.Encode()

	dialer := websocket.Dialer{Subprotocols: c.webSocketSubprotocols()}

	connection, response, err := dialer.DialContext(
		ctx,
		endpoint.String(),
		nil,
	)
	if response != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Debug(
				"closing the socket handshake body failed",
				"err", closeErr,
			)
		}
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "dial the control socket")
	}

	return connection, nil
}

// webSocketSubprotocols carries the bearer token, because a browser WebSocket
// cannot set an Authorization header. The token is base64url encoded so it is
// a legal subprotocol token.
func (c *Client) webSocketSubprotocols() []string {
	protocols := make([]string, 0, webSocketProtocolCapacity)
	protocols = append(protocols, webSocketSubprotocol)

	if c.token == "" {
		return protocols
	}

	return append(
		protocols,
		webSocketBearerPrefix+base64.RawURLEncoding.EncodeToString(
			[]byte(c.token),
		),
	)
}
