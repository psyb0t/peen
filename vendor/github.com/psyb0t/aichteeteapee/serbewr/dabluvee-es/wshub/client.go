package wshub

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
)

type Client struct {
	id            uuid.UUID              // UUID4 client identifier
	hub           Hub                    // Reference to hub
	hubMu         sync.RWMutex           // Protects hub field
	connections   *connectionsMap        // Thread-safe connection management
	sendCh        chan *dabluveees.Event // Client-level message channel
	doneCh        chan struct{}          // Client shutdown signal
	wg            sync.WaitGroup         // Wait for goroutines to finish
	stopOnce      sync.Once              // Ensure single stop
	config        ClientConfig           // Client configuration
	isStopped     atomic.Bool            // Atomic flag for client stopped state
	isRunning     atomic.Bool            // Atomic flag for client running state
	readyToStopCh chan struct{}          // Channel to signal ready to stop
}

// GetHubName safely returns the hub name, handling nil cases.
func (c *Client) GetHubName() string {
	c.hubMu.RLock()
	defer c.hubMu.RUnlock()

	if c.hub == nil {
		return "unknown"
	}

	return c.hub.Name()
}

func NewClient(opts ...ClientOption) *Client {
	return NewClientWithID(uuid.New(), opts...)
}

func NewClientWithID(clientID uuid.UUID, opts ...ClientOption) *Client {
	config := NewClientConfig()
	for _, opt := range opts {
		opt(&config)
	}

	client := &Client{
		id:            clientID,
		hub:           nil, // Hub will be set when added to hub
		connections:   newConnectionsMap(),
		sendCh:        make(chan *dabluveees.Event, config.SendBufferSize),
		doneCh:        make(chan struct{}),
		readyToStopCh: make(chan struct{}),
		config:        config,
	}

	slog.Debug(
		"created new client",
		aichteeteapee.FieldClientID, clientID,
	)

	return client
}

func (c *Client) ID() uuid.UUID {
	return c.id
}

func (c *Client) SetHub(hub Hub) {
	c.hubMu.Lock()
	defer c.hubMu.Unlock()

	c.hub = hub
}

func (c *Client) AddConnection(conn *Connection) {
	if c.isStopped.Load() {
		slog.Debug(
			"client is done, cannot add connection",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldConnectionID, conn.id,
		)

		return
	}

	slog.Debug(
		"adding new connection to client",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.id,
		aichteeteapee.FieldConnectionID, conn.id,
		aichteeteapee.FieldTotalConns, c.connections.Count()+1,
	)

	c.connections.Add(conn)

	c.wg.Go(func() {
		if conn.conn != nil {
			conn.readPump()
		}
	})

	c.wg.Go(func() {
		if conn.conn != nil {
			conn.writePump()
		}
	})

	slog.Debug(
		"connection pumps started",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.id,
		aichteeteapee.FieldConnectionID, conn.id,
	)
}

func (c *Client) RemoveConnection(connectionID uuid.UUID) {
	if c.isStopped.Load() {
		slog.Debug(
			"client is done, cannot remove connection",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldConnectionID, connectionID,
		)

		return
	}

	conn := c.connections.Remove(connectionID)
	if conn != nil {
		connectionCount := c.connections.Count()

		slog.Debug(
			"removed connection from client",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldConnectionID, connectionID,
			aichteeteapee.FieldTotalConns, connectionCount,
		)

		conn.Stop()

		// If no connections left, trigger client shutdown
		if connectionCount == 0 {
			slog.Debug(
				"no connections left, triggering client shutdown",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.id,
			)

			go c.Stop()
		}
	}
}

func (c *Client) GetConnections() map[uuid.UUID]*Connection {
	if c.connections == nil {
		return make(map[uuid.UUID]*Connection)
	}

	return c.connections.GetAll()
}

func (c *Client) ConnectionCount() int {
	if c.connections == nil {
		return 0
	}

	return c.connections.Count()
}

// SendEvent sends an event to all client connections
// (alias for Send for hub compatibility).
func (c *Client) SendEvent(event *dabluveees.Event) {
	c.Send(event)
}

// Send sends an event to the client's send channel for distribution.
func (c *Client) Send(event *dabluveees.Event) {
	if c.isStopped.Load() {
		slog.Debug(
			"client is done, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)

		return
	}

	if c.sendCh == nil || c.doneCh == nil {
		slog.Debug(
			"client channels are nil, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)

		return
	}

	select {
	case c.sendCh <- event:
		slog.Debug(
			"event queued for client distribution",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)
	case <-c.doneCh:
		slog.Debug(
			"client stopped, cannot send event",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
		)
	default:
		slog.Warn(
			"client send buffer full, dropping message",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
			aichteeteapee.FieldEventType, event.Type,
			aichteeteapee.FieldEventID, event.ID,
			aichteeteapee.FieldBufferSize, cap(c.sendCh),
		)
	}
}

// IsSubscribedTo checks if client is subscribed to an event type.
func (c *Client) IsSubscribedTo(_ dabluveees.EventType) bool {
	return true // For now, accept all events
}

// Stop gracefully shuts down the client and all its connections.
func (c *Client) Stop() {
	// Bail out if Run() was never called
	if !c.isRunning.Load() {
		return
	}

	// Wait for Run() to be ready for stopping
	<-c.readyToStopCh

	c.stopOnce.Do(func() {
		// Set done flag atomically first
		c.isStopped.Store(true)

		slog.Info(
			"stopping client",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)

		// Signal shutdown - check for nil channels first
		if c.doneCh != nil {
			close(c.doneCh)
		}

		if c.sendCh != nil {
			close(c.sendCh)
		}

		// Stop all connections - check for nil connections map first
		if c.connections != nil {
			for _, conn := range c.connections.GetAll() {
				conn.Stop()
			}
		}

		// Wait for goroutines to finish with timeout
		done := make(chan struct{})

		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			slog.Debug(
				"stopped on doneCh signal",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.id,
			)
		case <-time.After(5 * time.Second): //nolint:mnd
			// reasonable shutdown timeout
			slog.Warn(
				"stopped on timeout",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.id,
			)
		}
	})
}

// Run starts the client's distribution pump.
func (c *Client) Run() {
	// Set running flag at the very start
	c.isRunning.Store(true)
	defer c.isRunning.Store(false)

	if c.hub == nil {
		slog.Error(
			"client hub is nil, cannot run",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)

		return
	}

	if c.isStopped.Load() {
		slog.Debug(
			"client is stopped, cannot run",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)

		return
	}

	slog.Debug(
		"starting client distribution pump",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.id,
	)

	c.wg.Go(func() {
		c.distributionPump()
	})

	// Signal that we're ready to be stopped
	close(c.readyToStopCh)

	defer c.Stop()

	select {
	case <-c.doneCh:
		slog.Debug(
			"client stopped via done channel",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)
	case <-c.hub.Done():
		slog.Debug(
			"client stopped via hub.Done() channel",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)
	}
}

// distributionPump distributes events from sendCh to all connections.
//
//nolint:funlen // Event distribution loop requires comprehensive handling
func (c *Client) distributionPump() {
	defer func() {
		slog.Debug(
			"distribution pump stopped",
			aichteeteapee.FieldHubName, c.GetHubName(),
			aichteeteapee.FieldClientID, c.id,
		)
	}()

	slog.Debug(
		"starting client distribution pump",
		aichteeteapee.FieldHubName, c.GetHubName(),
		aichteeteapee.FieldClientID, c.id,
	)

	for {
		if c.isStopped.Load() {
			slog.Debug(
				"client is done, stopping distribution pump",
				aichteeteapee.FieldHubName, c.GetHubName(),
				aichteeteapee.FieldClientID, c.id,
			)

			return
		}

		select {
		case event, ok := <-c.sendCh:
			if !ok {
				return // Channel closed
			}

			if c.isStopped.Load() {
				slog.Debug(
					"client is done, cannot distribute event",
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.id,
					aichteeteapee.FieldEventType, event.Type,
					aichteeteapee.FieldEventID, event.ID,
				)

				return
			}

			connections := c.GetConnections()
			if len(connections) == 0 {
				slog.Debug(
					"no connections to distribute event to",
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.id,
					aichteeteapee.FieldEventType, event.Type,
					aichteeteapee.FieldEventID, event.ID,
				)

				continue
			}

			// Distribute to all connections
			for connID, conn := range connections {
				if c.isStopped.Load() {
					slog.Debug(
						"client done during distribution, stopping",
						aichteeteapee.FieldHubName, c.GetHubName(),
						aichteeteapee.FieldClientID, c.id,
					)

					return
				}

				conn.Send(event)

				slog.Debug(
					"event distributed to connection",
					aichteeteapee.FieldHubName, c.GetHubName(),
					aichteeteapee.FieldClientID, c.id,
					aichteeteapee.FieldEventType, event.Type,
					aichteeteapee.FieldEventID, event.ID,
					aichteeteapee.FieldConnectionID, connID,
				)
			}

		case <-c.doneCh:
			return
		}
	}
}
