package wshub

import (
	"time"

	"github.com/psyb0t/aichteeteapee"
)

type ClientConfig struct {
	SendBufferSize  int
	ReadBufferSize  int
	WriteBufferSize int
	ReadLimit       int64
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	PongTimeout     time.Duration
	// InvalidEventHandler receives messages that do not decode as an event.
	// Nil closes the connection with code 1007 on the first such message.
	InvalidEventHandler InvalidEventHandler
}

// NewClientConfig creates config with defaults from http/defaults.go.
func NewClientConfig() ClientConfig {
	return ClientConfig{
		SendBufferSize:  aichteeteapee.DefaultWebSocketClientSendBufferSize,
		ReadBufferSize:  aichteeteapee.DefaultWebSocketClientReadBufferSize,
		WriteBufferSize: aichteeteapee.DefaultWebSocketClientWriteBufferSize,
		ReadLimit:       aichteeteapee.DefaultWebSocketClientReadLimit,
		ReadTimeout:     aichteeteapee.DefaultWebSocketClientReadTimeout,
		WriteTimeout:    aichteeteapee.DefaultWebSocketClientWriteTimeout,
		PingInterval:    aichteeteapee.DefaultWebSocketClientPingInterval,
		PongTimeout:     aichteeteapee.DefaultWebSocketClientPongTimeout,
	}
}

type ClientOption func(*ClientConfig)

func WithSendBufferSize(size int) ClientOption {
	return func(c *ClientConfig) {
		c.SendBufferSize = size
	}
}

func WithReadBufferSize(size int) ClientOption {
	return func(c *ClientConfig) {
		c.ReadBufferSize = size
	}
}

func WithWriteBufferSize(size int) ClientOption {
	return func(c *ClientConfig) {
		c.WriteBufferSize = size
	}
}

func WithReadLimit(limit int64) ClientOption {
	return func(c *ClientConfig) {
		c.ReadLimit = limit
	}
}

// WithReadTimeout sets the timeout for reading messages from the WebSocket.
func WithReadTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReadTimeout = timeout
	}
}

// WithWriteTimeout sets the timeout for writing messages to the WebSocket.
func WithWriteTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.WriteTimeout = timeout
	}
}

// WithPingInterval sets the interval for sending ping messages.
func WithPingInterval(interval time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.PingInterval = interval
	}
}

// WithPongTimeout sets the timeout for waiting for pong responses.
func WithPongTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.PongTimeout = timeout
	}
}

// WithInvalidEventHandler sets the handler for messages that do not decode
// as an event: malformed JSON, an id that is not a UUID, or a field of the
// wrong type. When the handler returns nil the connection keeps reading.
// When it returns an error, or no handler is set, the connection closes with
// code 1007 (invalid frame payload data).
func WithInvalidEventHandler(handler InvalidEventHandler) ClientOption {
	return func(c *ClientConfig) {
		c.InvalidEventHandler = handler
	}
}
