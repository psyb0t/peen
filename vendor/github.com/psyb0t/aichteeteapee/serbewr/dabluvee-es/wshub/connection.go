package wshub

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
)

const (
	// invalidEventCloseText is the close reason for an undecodable message.
	// It is fixed so client bytes never reach the close frame.
	invalidEventCloseText = "invalid event"
	invalidEventReason    = "invalid_event"
)

type Connection struct {
	id       uuid.UUID              // UUID4 connection identifier
	conn     *websocket.Conn        // WebSocket connection
	client   *Client                // Reference to parent client
	sendCh   chan *dabluveees.Event // Per-connection message channel
	doneCh   chan struct{}          // Connection shutdown signal
	stopOnce sync.Once              // Ensure single stop
	isDone   atomic.Bool            // Atomic flag for connection state
	sendWg   sync.WaitGroup         // Wait for in-flight sends to complete
}

// NewConnection creates a new WebSocket connection.
func NewConnection(
	conn *websocket.Conn,
	client *Client,
) *Connection {
	return &Connection{
		id:       uuid.New(),
		conn:     conn,
		client:   client,
		sendCh:   make(chan *dabluveees.Event, client.config.SendBufferSize),
		doneCh:   make(chan struct{}),
		stopOnce: sync.Once{},
	}
}

// GetHubName safely returns the hub name, handling nil cases.
func (c *Connection) GetHubName() string {
	if c.client == nil {
		return "unknown"
	}

	return c.client.GetHubName()
}

// GetClientID safely returns the client ID, handling nil cases.
func (c *Connection) GetClientID() uuid.UUID {
	if c.client == nil {
		return uuid.Nil
	}

	return c.client.id
}

// Send sends an event to the connection's send channel.
func (c *Connection) Send(event *dabluveees.Event) {
	if c.isDone.Load() {
		slog.Debug(
			"connection is done, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)

		return
	}

	if c.sendCh == nil || c.doneCh == nil {
		slog.Debug(
			"connection channels are nil, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)

		return
	}

	// Add to WaitGroup to track this send operation
	c.sendWg.Add(1)
	defer c.sendWg.Done()

	// Check again after acquiring WaitGroup (race protection)
	if c.isDone.Load() {
		slog.Debug(
			"connection became done during send, aborting",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		return
	}

	select {
	case c.sendCh <- event:
		slog.Debug(
			"event queued for sending",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)
	case <-c.doneCh:
		slog.Debug(
			"connection stopped, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)
	}
}

// Stop cleanly shuts down the connection.
func (c *Connection) Stop() {
	c.stopOnce.Do(func() {
		// Set done flag atomically first
		c.isDone.Store(true)

		slog.Debug(
			"stopping connection",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		// Close doneCh first to signal shutdown
		if c.doneCh != nil {
			close(c.doneCh)
		}

		// Wait for all in-flight sends to complete before closing sendCh
		slog.Debug(
			"waiting for in-flight sends to complete",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		c.sendWg.Wait()

		// Now safe to close sendCh - no more sends can happen
		if c.sendCh != nil {
			close(c.sendCh)
		}

		if c.conn != nil {
			_ = c.conn.Close()
		}

		slog.Debug(
			"connection stop completed",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)
	})
}

// writePump handles outbound messages and keepalive.
func (c *Connection) writePump() { //nolint:cyclop,funlen
	defer func() {
		slog.Debug(
			"write pump stopped",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		c.client.RemoveConnection(c.id) // Auto-cleanup
	}()

	if c.isDone.Load() {
		slog.Debug(
			"connection is done, cannot start write pump",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		return
	}

	slog.Debug(
		"starting connection write pump",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.GetClientID(),
		aichteeteapee.FieldConnectionID, c.id,
	)

	ticker := time.NewTicker(c.client.config.PingInterval)
	defer ticker.Stop()

	for {
		if c.isDone.Load() {
			slog.Debug(
				"connection is done, stopping write pump",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.GetClientID(),
				aichteeteapee.FieldConnectionID, c.id,
			)

			return
		}

		select {
		case event, ok := <-c.sendCh:
			if !ok {
				// Channel closed, send close message
				_ = c.conn.SetWriteDeadline(
					time.Now().Add(c.client.config.WriteTimeout),
				)
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})

				return
			}

			_ = c.conn.SetWriteDeadline(
				time.Now().Add(c.client.config.WriteTimeout),
			)

			if err := c.conn.WriteJSON(event); err != nil {
				if websocket.IsCloseError(
					err, websocket.CloseNormalClosure, websocket.CloseGoingAway,
				) {
					slog.Info(
						"connection closed normally during write",
						aichteeteapee.FieldHubName, c.GetHubName(),
						aichteeteapee.FieldClientID, c.GetClientID(),
						aichteeteapee.FieldConnectionID, c.id,
					)

					return
				}

				slog.Error(
					"connection write error",
					"error", err,
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.GetClientID(),
					aichteeteapee.FieldConnectionID, c.id,
				)

				return
			}

			slog.Debug(
				"event sent to connection",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.GetClientID(),
				aichteeteapee.FieldConnectionID, c.id,
				aichteeteapee.FieldEventType, string(event.Type),
				aichteeteapee.FieldEventID, event.ID,
			)

		case <-ticker.C:
			// Send ping
			_ = c.conn.SetWriteDeadline(
				time.Now().Add(c.client.config.WriteTimeout),
			)

			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			if err != nil {
				if websocket.IsCloseError(
					err, websocket.CloseNormalClosure, websocket.CloseGoingAway,
				) {
					slog.Info(
						"connection closed normally during ping",
						aichteeteapee.FieldHubName, c.GetHubName(),
						aichteeteapee.FieldClientID, c.GetClientID(),
						aichteeteapee.FieldConnectionID, c.id,
					)

					return
				}

				slog.Error(
					"connection ping error",
					"error", err,
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.GetClientID(),
					aichteeteapee.FieldConnectionID, c.id,
				)

				return
			}

		case <-c.doneCh:
			return
		}
	}
}

// handleInvalidEvent passes an undecodable message to the client's
// InvalidEventHandler and reports whether the read pump should keep reading.
// Otherwise it sends a 1007 close frame with a fixed reason. Neither the
// close frame nor the log carries the client's bytes or the decode error.
func (c *Connection) handleInvalidEvent(data []byte, decodeErr error) bool {
	slog.Warn(
		"invalid event received from connection",
		aichteeteapee.FieldReason, invalidEventReason,
		aichteeteapee.FieldBytes, len(data),
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.GetClientID(),
		aichteeteapee.FieldConnectionID, c.id,
	)

	handler := c.client.config.InvalidEventHandler
	if handler != nil {
		c.client.hubMu.RLock()
		hub := c.client.hub
		c.client.hubMu.RUnlock()

		if handler(hub, c.client, data, decodeErr) == nil {
			return true
		}

		// The handler's error may wrap the decode error, which can quote
		// client input, so it is not logged.
		slog.Warn(
			"invalid event handler closed connection",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)
	}

	// WriteControl is safe to call alongside the write pump.
	_ = c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(
			websocket.CloseInvalidFramePayloadData, invalidEventCloseText,
		),
		time.Now().Add(c.client.config.WriteTimeout),
	)

	return false
}

// readPump handles inbound messages and connection monitoring.
func (c *Connection) readPump() { //nolint:cyclop,funlen
	defer func() {
		slog.Debug(
			"read pump stopped",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		c.client.RemoveConnection(c.id) // Auto-cleanup
	}()

	if c.isDone.Load() {
		slog.Debug(
			"connection is done, cannot start read pump",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)

		return
	}

	slog.Debug(
		"starting connection read pump",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.GetClientID(),
		aichteeteapee.FieldConnectionID, c.id,
	)

	// Configure connection
	c.conn.SetReadLimit(c.client.config.ReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.client.config.ReadTimeout))

	// Set pong handler
	c.conn.SetPongHandler(func(string) error {
		if c.isDone.Load() {
			return websocket.ErrCloseSent
		}

		_ = c.conn.SetReadDeadline(time.Now().Add(c.client.config.PongTimeout))

		return nil
	})

	for {
		if c.isDone.Load() {
			slog.Debug(
				"connection is done, stopping read pump",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.GetClientID(),
				aichteeteapee.FieldConnectionID, c.id,
			)

			return
		}

		select {
		case <-c.doneCh:
			return
		default:
		}

		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				slog.Info(
					"websocket connection closed",
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.GetClientID(),
					aichteeteapee.FieldConnectionID, c.id,
				)

				return
			}

			slog.Error(
				"connection read error",
				"error", err,
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.GetClientID(),
				aichteeteapee.FieldConnectionID, c.id,
			)

			return
		}

		var event dabluveees.Event
		if err := json.Unmarshal(data, &event); err != nil {
			if !c.handleInvalidEvent(data, err) {
				return
			}

			_ = c.conn.SetReadDeadline(
				time.Now().Add(c.client.config.ReadTimeout),
			)

			continue
		}

		// Check if done after reading event
		if c.isDone.Load() {
			slog.Debug(
				"connection is done, cannot process event",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.GetClientID(),
				aichteeteapee.FieldConnectionID, c.id,
				aichteeteapee.FieldEventType, string(event.Type),
				aichteeteapee.FieldEventID, event.ID,
			)

			return
		}

		// Reset read deadline
		_ = c.conn.SetReadDeadline(time.Now().Add(c.client.config.ReadTimeout))

		slog.Debug(
			"event received from connection",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
			aichteeteapee.FieldEventType, string(event.Type),
			aichteeteapee.FieldEventID, event.ID,
		)

		// Process event through hub
		if c.client.hub != nil {
			c.client.hub.ProcessEvent(c.client, &event)

			continue
		}

		slog.Debug(
			"hub is nil, cannot process event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.GetClientID(),
			aichteeteapee.FieldConnectionID, c.id,
		)
	}
}
