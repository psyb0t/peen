package dabluveees

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/psyb0t/aichteeteapee"
)

// UpgradeHandlerConfig holds WebSocket upgrade handler configuration.
type UpgradeHandlerConfig struct {
	ReadBufferSize    int
	WriteBufferSize   int
	HandshakeTimeout  time.Duration
	CheckOrigin       func(*http.Request) bool
	Subprotocols      []string
	EnableCompression bool
}

// NewUpgradeHandlerConfig creates config with defaults from http/defaults.go.
func NewUpgradeHandlerConfig() UpgradeHandlerConfig {
	config := UpgradeHandlerConfig{
		ReadBufferSize:  aichteeteapee.DefaultWebSocketHandlerReadBufferSize,
		WriteBufferSize: aichteeteapee.DefaultWebSocketHandlerWriteBufferSize,
		HandshakeTimeout: aichteeteapee.
			DefaultWebSocketHandlerHandshakeTimeout,
		EnableCompression: aichteeteapee.
			DefaultWebSocketHandlerEnableCompression,
		Subprotocols: []string{},
		CheckOrigin:  aichteeteapee.GetDefaultWebSocketCheckOrigin,
	}

	slog.Debug(
		"created websocket handler config with defaults",
		aichteeteapee.FieldReadBufferSize, config.ReadBufferSize,
		aichteeteapee.FieldWriteBufferSize, config.WriteBufferSize,
		aichteeteapee.FieldHandshakeTimeout, config.HandshakeTimeout,
		aichteeteapee.FieldEnableCompression, config.EnableCompression,
	)

	return config
}
