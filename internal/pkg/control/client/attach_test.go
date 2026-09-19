package client_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	attachTestSubprotocol = "peen.v1"
	// attachTestBearerPrefix is a public protocol label, not a credential.
	//nolint:gosec // The token follows this prefix, the prefix is not secret.
	attachTestBearerPrefix = "peen.bearer."
	attachTestEventType    = "turn.completed"
	attachTestTimeout      = 5 * time.Second
)

// attachStub records the handshake and pushes events at an attached client.
// The handler runs on the server goroutine, so the recorded handshake is
// guarded rather than read across goroutines unsynchronized.
type attachStub struct {
	events []string

	mutex           sync.Mutex
	gotSessionID    string
	gotSubprotocols []string
}

func (s *attachStub) record(sessionID string, subprotocols []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.gotSessionID = sessionID
	s.gotSubprotocols = subprotocols
}

func (s *attachStub) handshake() (string, []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.gotSessionID, s.gotSubprotocols
}

func (s *attachStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(
			r.URL.Query().Get("sessionId"),
			websocket.Subprotocols(r),
		)

		upgrader := websocket.Upgrader{
			Subprotocols: []string{attachTestSubprotocol},
		}

		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		defer func() {
			_ = connection.Close()
		}()

		for _, event := range s.events {
			if writeErr := connection.WriteMessage(
				websocket.TextMessage,
				[]byte(event),
			); writeErr != nil {
				return
			}
		}

		// Close normally so the client returns without reporting a failure.
		_ = connection.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
	})
}

func newAttachClient(
	t *testing.T,
	stub *attachStub,
	token string,
) *controlclient.Client {
	t.Helper()

	server := httptest.NewServer(stub.handler())
	t.Cleanup(server.Close)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		Token:         token,
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)

	return client
}

// Attaching asks the server for one session's events, so a client does not
// receive every session's feed and discard most of it.
func TestAttachRequestsTheServerSideSessionFilter(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	stub := &attachStub{
		events: []string{
			`{"id":"` + uuid.New().String() + `","type":"` +
				attachTestEventType + `","data":{"ok":true}}`,
		},
	}

	client := newAttachClient(t, stub, testToken)

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	received := []controlclient.AttachEvent{}
	require.NoError(t, client.Attach(
		ctx,
		sessionID,
		func(event controlclient.AttachEvent) error {
			received = append(received, event)

			return nil
		},
	))

	gotSessionID, _ := stub.handshake()
	assert.Equal(t, sessionID.String(), gotSessionID)

	require.Len(t, received, 1)
	assert.Equal(t, attachTestEventType, received[0].Type)
	assert.Contains(t, string(received[0].Data), "ok")
}

// A browser socket cannot set an Authorization header, so the token rides in
// the subprotocol list.
func TestAttachCarriesTheTokenAsASubprotocol(t *testing.T) {
	t.Parallel()

	stub := &attachStub{}
	client := newAttachClient(t, stub, testToken)

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		func(controlclient.AttachEvent) error { return nil },
	))

	wantBearer := attachTestBearerPrefix +
		base64.RawURLEncoding.EncodeToString([]byte(testToken))

	_, gotSubprotocols := stub.handshake()
	assert.Contains(t, gotSubprotocols, attachTestSubprotocol)
	assert.Contains(t, gotSubprotocols, wantBearer)
}

// A deployment with no token sends no bearer subprotocol at all.
func TestAttachSendsNoBearerWithoutAToken(t *testing.T) {
	t.Parallel()

	stub := &attachStub{}
	client := newAttachClient(t, stub, "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		func(controlclient.AttachEvent) error { return nil },
	))

	_, gotSubprotocols := stub.handshake()
	assert.Equal(t, []string{attachTestSubprotocol}, gotSubprotocols)
}

// Cancelling the context ends the stream without reporting a read failure.
func TestAttachStopsCleanlyWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	held := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			Subprotocols: []string{attachTestSubprotocol},
		}

		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		// Hold the socket open with no traffic so only cancellation ends it.
		_, _, _ = connection.ReadMessage()

		_ = connection.Close()
	})

	server := httptest.NewServer(held)
	t.Cleanup(server.Close)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		done <- client.Attach(
			ctx,
			uuid.New(),
			func(controlclient.AttachEvent) error { return nil },
		)
	}()

	cancel()

	select {
	case attachErr := <-done:
		require.NoError(t, attachErr)
	case <-time.After(attachTestTimeout):
		t.Fatal("attach did not stop when its context ended")
	}
}

func TestAttachRejectsAMissingSessionID(t *testing.T) {
	t.Parallel()

	stub := &attachStub{}
	client := newAttachClient(t, stub, testToken)

	err := client.Attach(
		t.Context(),
		uuid.Nil,
		func(controlclient.AttachEvent) error { return nil },
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
