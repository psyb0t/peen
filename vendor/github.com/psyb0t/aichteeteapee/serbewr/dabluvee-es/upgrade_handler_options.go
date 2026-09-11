package dabluveees

import (
	"net/http"
	"time"
)

type UpgradeHandlerOption func(*UpgradeHandlerConfig)

// WithUpgradeHandlerBufferSizes sets both read and write buffer sizes
// for the WebSocket upgrader.
func WithUpgradeHandlerBufferSizes(read, write int) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		c.ReadBufferSize = read
		c.WriteBufferSize = write
	}
}

// WithUpgradeHandlerHandshakeTimeout sets the WebSocket handshake timeout.
func WithUpgradeHandlerHandshakeTimeout(
	timeout time.Duration,
) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		c.HandshakeTimeout = timeout
	}
}

// WithUpgradeHandlerCompression enables or disables WebSocket compression.
func WithUpgradeHandlerCompression(enable bool) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		c.EnableCompression = enable
	}
}

// WithUpgradeHandlerSubprotocols sets the supported WebSocket subprotocols.
func WithUpgradeHandlerSubprotocols(protocols ...string) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		c.Subprotocols = protocols
	}
}

// WithUpgradeHandlerCheckOrigin sets the origin checking function for
// WebSocket connections.
func WithUpgradeHandlerCheckOrigin(
	checkOrigin func(*http.Request) bool,
) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		c.CheckOrigin = checkOrigin
	}
}
