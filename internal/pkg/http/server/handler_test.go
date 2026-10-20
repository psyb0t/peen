package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/peen/internal/pkg/memlistener"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeListenerServesHTTPAndWebSocketUntilCancellation(t *testing.T) {
	sessionID := uuid.New()
	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
	})
	require.NoError(t, err)
	listener := memlistener.New()
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- instance.ServeListener(ctx, listener) }()
	transport := &http.Transport{DialContext: listener.DialContext}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{
		Transport: transport,
		Timeout:   testWebSocketEventuallyTimeout,
	}
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://in-process/ready",
		nil,
	)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	dialer := websocket.Dialer{
		NetDialContext:   listener.DialContext,
		Subprotocols:     []string{webSocketSubprotocol},
		HandshakeTimeout: testWebSocketEventuallyTimeout,
	}
	socket, upgraded, err := dialer.DialContext(
		t.Context(),
		"ws://in-process/ws/v1",
		nil,
	)
	if upgraded != nil {
		require.NoError(t, upgraded.Body.Close())
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, socket.Close()) })
	assert.Equal(t, webSocketSubprotocol, socket.Subprotocol())
	waitForWebSocketClients(t, instance, sessionID, 1)
	require.NoError(t, socket.SetReadDeadline(
		time.Now().Add(testWebSocketEventuallyTimeout),
	))
	cancel()
	select {
	case serveErr := <-done:
		require.NoError(t, serveErr)
	case <-time.After(testWebSocketEventuallyTimeout):
		t.Fatal("in-process HTTP server did not stop")
	}
	_, _, err = socket.ReadMessage()
	require.Error(t, err)
}

func TestInProcessUpgradeGateDrainsAndRefusesNewUpgrades(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	gate := &inProcessUpgradeGate{
		handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			if request.URL.Path == webSocketPath {
				close(entered)
				<-release
				close(returned)
			}
			writer.WriteHeader(http.StatusOK)
		}),
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, webSocketPath, nil,
		))
	}()
	select {
	case <-entered:
	case <-time.After(testWebSocketEventuallyTimeout):
		t.Fatal("upgrade handler did not start")
	}
	drained := make(chan struct{})
	go func() {
		gate.stopUpgrades()
		// Draining must observe the active handler's return before it completes.
		select {
		case <-returned:
		default:
			t.Error("upgrade drain completed before its active handler")
		}
		close(drained)
	}()
	close(release)
	select {
	case <-drained:
	case <-time.After(testWebSocketEventuallyTimeout):
		t.Fatal("upgrade handlers did not drain")
	}
	<-firstDone
	for _, tc := range []struct {
		path   string
		status int
	}{
		{path: webSocketPath, status: http.StatusServiceUnavailable},
		{path: readyPath, status: http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, tc.path, nil,
		))
		assert.Equal(t, tc.status, recorder.Code)
	}
}
