package client

import (
	"context"
	"encoding/base64"
	"net/url"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	webSocketPath        = "/v1/ws"
	webSocketScheme      = "ws"
	webSocketSubprotocol = "peen.v1"
	// webSocketBearerPrefix is a public protocol label, not a credential.
	//nolint:gosec // The token follows this prefix, the prefix is not secret.
	webSocketBearerPrefix      = "peen.bearer."
	webSocketSessionIDQueryKey = "sessionId"

	// webSocketProtocolCapacity covers the subprotocol plus an optional bearer.
	webSocketProtocolCapacity = 2
)

// AttachEvent is one server event delivered to an attached client.
type AttachEvent struct {
	Type string
	Data []byte
}

// Attach streams the controller's event feed for one session until the context
// ends or the controller closes the socket.
//
// The filter is server side: the socket asks for one session's events rather
// than receiving every session's and discarding most. Attaching is read only,
// so it never starts a turn.
func (c *Client) Attach(
	ctx context.Context,
	sessionID uuid.UUID,
	sink func(AttachEvent) error,
) error {
	if sessionID == uuid.Nil {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"attach requires a session ID",
		)
	}

	connection, err := c.dialWebSocket(ctx, sessionID)
	if err != nil {
		// Cancelling while the handshake is still in flight is the same clean
		// stop as cancelling mid-stream, so it is not reported as a failure.
		if ctx.Err() != nil {
			return nil
		}

		return err
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

	return streamAttachedEvents(ctx, connection, sink)
}

// streamAttachedEvents reads until the socket closes or the context ends.
func streamAttachedEvents(
	ctx context.Context,
	connection *websocket.Conn,
	sink func(AttachEvent) error,
) error {
	for {
		event := dabluveees.Event{}
		if err := connection.ReadJSON(&event); err != nil {
			// A cancelled attach closes the socket itself, so the read failure
			// it produces is the clean stop, not a fault.
			if ctx.Err() != nil {
				return nil //nolint:nilerr // Cancellation is a clean stop.
			}

			if websocket.IsCloseError(
				err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
			) {
				return nil
			}

			return ctxerrors.Wrap(err, "read an attached session event")
		}

		if err := sink(AttachEvent{
			Type: string(event.Type),
			Data: event.Data,
		}); err != nil {
			return ctxerrors.Wrap(err, "deliver an attached session event")
		}
	}
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
