// Package handshake keeps the v1 WebSocket handshake contract.
package handshake

import (
	"net/http"
	"slices"

	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
)

const webSocketVersion = "13"

// Open keeps the configured server preference when selecting a subprotocol.
func Open(
	w http.ResponseWriter,
	r *http.Request,
	config dabluveees.UpgradeHandlerConfig,
) (*websocket.Conn, error) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:    config.ReadBufferSize,
		WriteBufferSize:   config.WriteBufferSize,
		HandshakeTimeout:  config.HandshakeTimeout,
		CheckOrigin:       config.CheckOrigin,
		Subprotocols:      selectSubprotocols(r, config.Subprotocols),
		EnableCompression: config.EnableCompression,
		Error:             writeError,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "upgrade WebSocket connection")
	}

	return conn, nil
}

func selectSubprotocols(r *http.Request, supported []string) []string {
	if len(supported) == 0 {
		return nil
	}

	offered := websocket.Subprotocols(r)
	for i, protocol := range supported {
		if slices.Contains(offered, protocol) {
			return supported[i : i+1]
		}
	}

	return nil
}

func writeError(w http.ResponseWriter, _ *http.Request, status int, _ error) {
	if status == http.StatusUpgradeRequired {
		w.Header().Del(aichteeteapee.HeaderNameUpgrade)

		status = http.StatusBadRequest
	}

	// Preserve Gorilla's original response format for existing v1 clients.
	w.Header().Set(
		aichteeteapee.HeaderNameSecWebSocketVersion, webSocketVersion,
	)
	http.Error(w, http.StatusText(status), status)
}
