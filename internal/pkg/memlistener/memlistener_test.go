package memlistener

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPayload           = "ping"
	testReadHeaderTimeout = 5 * time.Second
)

func TestDialReachesAccept(t *testing.T) {
	t.Parallel()

	listener := New()
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	accepted := make(chan net.Conn, 1)

	go func() {
		connection, err := listener.Accept()
		if err != nil {
			close(accepted)

			return
		}

		accepted <- connection
	}()

	client, err := listener.DialContext(t.Context(), network, "")
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, client.Close()) })

	server, ok := <-accepted
	require.True(t, ok)

	t.Cleanup(func() { require.NoError(t, server.Close()) })

	written := make(chan error, 1)

	go func() {
		_, writeErr := client.Write([]byte(testPayload))
		written <- writeErr
	}()

	received := make([]byte, len(testPayload))
	_, err = io.ReadFull(server, received)
	require.NoError(t, err)
	require.NoError(t, <-written)
	assert.Equal(t, testPayload, string(received))
}

func TestClosedListenerRefusesDialAndAccept(t *testing.T) {
	t.Parallel()

	listener := New()
	require.NoError(t, listener.Close())
	require.NoError(t, listener.Close())

	_, err := listener.Accept()
	require.ErrorIs(t, err, net.ErrClosed)

	_, err = listener.DialContext(t.Context(), network, "")
	require.ErrorIs(t, err, net.ErrClosed)
}

func TestDialStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	listener := New()
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := listener.DialContext(ctx, network, "")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAddrIsNotANetworkAddress(t *testing.T) {
	t.Parallel()

	listener := New()
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	assert.Equal(t, network, listener.Addr().Network())
	assert.Equal(t, network, listener.Addr().String())
}

func TestHTTPServesOverTheListener(t *testing.T) {
	t.Parallel()

	listener := New()

	server := &http.Server{
		Handler: http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(testPayload))
				assert.NoError(t, err)
			},
		),
		ReadHeaderTimeout: testReadHeaderTimeout,
	}

	served := make(chan error, 1)

	go func() { served <- server.Serve(listener) }()

	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.ErrorIs(t, <-served, http.ErrServerClosed)
	})

	client := &http.Client{
		Transport: &http.Transport{DialContext: listener.DialContext},
	}

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+network+"/",
		nil,
	)
	require.NoError(t, err)

	response, err := client.Do(request)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, testPayload, string(body))
}
