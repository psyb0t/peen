package wshub

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
)

// UpgradeHandler creates an HTTP handler that upgrades connections
// to WebSocket.
func UpgradeHandler( //nolint:funlen
	hub Hub,
	opts ...UpgradeHandlerOption,
) http.HandlerFunc {
	config := NewUpgradeHandlerConfig()

	for _, opt := range opts {
		opt(&config)
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:    config.ReadBufferSize,
		WriteBufferSize:   config.WriteBufferSize,
		HandshakeTimeout:  config.HandshakeTimeout,
		CheckOrigin:       config.CheckOrigin,
		Subprotocols:      config.Subprotocols,
		EnableCompression: config.EnableCompression,
	}

	slog.Debug(
		"created websocket upgrade handler",
		aichteeteapee.FieldReadBufferSize, config.ReadBufferSize,
		aichteeteapee.FieldWriteBufferSize, config.WriteBufferSize,
		aichteeteapee.FieldHandshakeTimeout, config.HandshakeTimeout,
		aichteeteapee.FieldEnableCompression, config.EnableCompression,
		aichteeteapee.FieldHubName, hub.Name(),
	)

	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get(
			aichteeteapee.HeaderNameOrigin,
		)

		slog.Debug(
			"websocket upgrade request received",
			aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
			aichteeteapee.FieldOrigin, origin,
			aichteeteapee.FieldUserAgent, r.UserAgent(),
			aichteeteapee.FieldEndpoint, r.URL.Path,
		)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error(
				"websocket upgrade failed",
				"error", err,
				aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
				aichteeteapee.FieldOrigin, origin,
			)

			return // upgrader already wrote HTTP error response
		}

		conn.SetCloseHandler(func(code int, text string) error {
			slog.Debug(
				"websocket close handler triggered",
				aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
				aichteeteapee.FieldOrigin, origin,
				aichteeteapee.FieldCloseCode, code,
				aichteeteapee.FieldCloseText, text,
			)

			return nil
		})

		slog.Info(
			"websocket connection established",
			aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
			aichteeteapee.FieldOrigin, origin,
		)

		var client *Client

		clientID := extractClientIDFromRequest(r)
		if clientID != "" {
			parsedClientID := parseClientID(clientID)

			var wasCreated bool

			client, wasCreated = hub.GetOrCreateClient(
				parsedClientID, config.ClientOptions...,
			)

			if !wasCreated {
				slog.Debug(
					"adding connection to existing client",
					aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
					aichteeteapee.FieldOrigin, origin,
					aichteeteapee.FieldClientID, parsedClientID,
				)
			} else {
				slog.Debug(
					"created new client with specified ID",
					aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
					aichteeteapee.FieldOrigin, origin,
					aichteeteapee.FieldClientID, parsedClientID,
				)
			}

			connection := NewConnection(conn, client)
			client.AddConnection(connection)
		} else {
			slog.Debug(
				"creating client with generated ID",
				aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
				aichteeteapee.FieldOrigin, origin,
			)

			client = NewClient(config.ClientOptions...)
			hub.AddClient(client)

			connection := NewConnection(conn, client)
			client.AddConnection(connection)
		}

		slog.Debug(
			"client ready",
			aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
			aichteeteapee.FieldOrigin, origin,
			aichteeteapee.FieldClientID, client.ID(),
		)

		slog.Debug(
			"client connection handled successfully",
			aichteeteapee.FieldRemoteAddr, r.RemoteAddr,
			aichteeteapee.FieldOrigin, origin,
			aichteeteapee.FieldClientID, client.ID(),
		)
	}
}

func extractClientIDFromRequest(r *http.Request) string {
	if clientID := r.URL.Query().Get("clientID"); clientID != "" {
		return clientID
	}

	clientID := r.Header.Get(
		aichteeteapee.HeaderNameXClientID,
	)
	if clientID != "" {
		return clientID
	}

	return ""
}

// parseClientID returns a parsed UUID or generates a new one on parse failure.
func parseClientID(clientID string) uuid.UUID {
	if parsedID, err := uuid.Parse(clientID); err == nil {
		return parsedID
	}

	slog.Warn(
		"invalid client ID format, generating new UUID",
		"providedClientID", clientID,
	)

	return uuid.New()
}
