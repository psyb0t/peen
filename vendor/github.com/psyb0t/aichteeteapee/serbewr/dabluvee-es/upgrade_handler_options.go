package dabluveees

import (
	"log/slog"
	"net/http"
	"time"
)

type UpgradeHandlerOption func(*UpgradeHandlerConfig)

// WithUpgradeHandlerBufferSizes sets both read and write buffer sizes
// for the WebSocket upgrader.
func WithUpgradeHandlerBufferSizes(read, write int) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		slog.Debug(
			"updating handler buffer sizes",
			"oldReadSize", c.ReadBufferSize,
			"oldWriteSize", c.WriteBufferSize,
			"newReadSize", read,
			"newWriteSize", write,
		)

		c.ReadBufferSize = read
		c.WriteBufferSize = write
	}
}

// WithUpgradeHandlerHandshakeTimeout sets the WebSocket handshake timeout.
func WithUpgradeHandlerHandshakeTimeout(
	timeout time.Duration,
) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		slog.Debug(
			"updating handler handshake timeout",
			"oldTimeout", c.HandshakeTimeout,
			"newTimeout", timeout,
		)

		c.HandshakeTimeout = timeout
	}
}

// WithUpgradeHandlerCompression enables or disables WebSocket compression.
func WithUpgradeHandlerCompression(enable bool) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		slog.Debug(
			"updating handler compression setting",
			"oldCompression", c.EnableCompression,
			"newCompression", enable,
		)

		c.EnableCompression = enable
	}
}

// WithUpgradeHandlerSubprotocols sets the supported WebSocket subprotocols.
func WithUpgradeHandlerSubprotocols(protocols ...string) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		slog.Debug(
			"updating handler subprotocols",
			"oldProtocols", c.Subprotocols,
			"newProtocols", protocols,
		)

		c.Subprotocols = protocols
	}
}

// WithUpgradeHandlerCheckOrigin sets the origin checking function for
// WebSocket connections.
func WithUpgradeHandlerCheckOrigin(
	checkOrigin func(*http.Request) bool,
) UpgradeHandlerOption {
	return func(c *UpgradeHandlerConfig) {
		slog.Debug("updating handler CheckOrigin function")

		c.CheckOrigin = checkOrigin
	}
}
