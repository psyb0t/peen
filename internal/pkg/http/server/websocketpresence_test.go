package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type presenceTestRuntime struct {
	*testRuntime
	cancelled    chan uuid.UUID
	cancelErr    error
	beforeCancel func(context.Context, uuid.UUID)
}

func (r *presenceTestRuntime) CancelSession(
	ctx context.Context,
	id uuid.UUID,
) (*api.CancelResponse, error) {
	if r.beforeCancel != nil {
		r.beforeCancel(ctx, id)
	}

	r.cancelled <- id
	if r.cancelErr != nil {
		return nil, r.cancelErr
	}

	return &api.CancelResponse{CancelRequested: true}, nil
}

func TestWebSocketPresenceKeepsSlowStopWithinItsSession(t *testing.T) {
	t.Parallel()
	id, otherID := uuid.New(), uuid.New()
	stopping, release := make(chan struct{}), make(chan struct{})
	runtime := &presenceTestRuntime{
		testRuntime: newTestRuntime(id), cancelled: make(chan uuid.UUID, 2),
		beforeCancel: func(ctx context.Context, sessionID uuid.UUID) {
			if sessionID != id {
				return
			}

			close(stopping)
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	}
	router := &presenceTestRouter{
		testTurnRouter: &testTurnRouter{runtime: runtime.testRuntime},
		cancelled:      make(chan uuid.UUID, 2),
	}
	instance, err := newTestServer(Dependencies{
		Runtime: runtime, Turns: router, APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)
	detach := instance.attachWebSocketPresence(t.Context(), uuid.New(), id)
	stopped := make(chan struct{})
	go func() { defer close(stopped); detach() }()
	t.Cleanup(func() { close(release); <-stopped })
	select {
	case <-stopping:
	case <-time.After(time.Second):
		t.Fatal("session stop did not start")
	}

	connected := make(chan struct{})
	go func() {
		instance.attachWebSocketPresence(t.Context(), uuid.New(), otherID)
		close(connected)
	}()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("one session stop blocked another session connection")
	}
}

type presenceTestRouter struct {
	*testTurnRouter
	cancelled chan uuid.UUID
	cancelErr error
}

func (r *presenceTestRouter) CancelSessionTurn(
	_ context.Context,
	id uuid.UUID,
) (bool, error) {
	r.cancelled <- id
	if r.cancelErr != nil {
		return false, r.cancelErr
	}

	return true, nil
}

func TestUnattendedSessionAttemptsBothStopBoundaries(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		durableErr error
		workerErr  error
	}{
		{name: "success"},
		{name: "durable failure", durableErr: commerr.ErrConflict},
		{name: "worker failure", workerErr: commerr.ErrFetchFailed},
		{
			name: "both fail", durableErr: commerr.ErrConflict,
			workerErr: commerr.ErrFetchFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			runtime := &presenceTestRuntime{
				testRuntime: newTestRuntime(id),
				cancelled:   make(chan uuid.UUID, 1), cancelErr: tc.durableErr,
			}
			router := &presenceTestRouter{
				testTurnRouter: &testTurnRouter{runtime: runtime.testRuntime},
				cancelled:      make(chan uuid.UUID, 1), cancelErr: tc.workerErr,
			}
			instance := &Server{deps: Dependencies{Runtime: runtime, Turns: router}}
			err := instance.stopUnattendedSession(t.Context(), id)
			if tc.durableErr != nil {
				require.ErrorIs(t, err, tc.durableErr)
			}
			if tc.workerErr != nil {
				require.ErrorIs(t, err, tc.workerErr)
			}
			if tc.durableErr == nil && tc.workerErr == nil {
				require.NoError(t, err)
			}

			assert.Len(t, runtime.cancelled, 1)
			assert.Len(t, router.cancelled, 1)
		})
	}
}

func TestWebSocketPresenceStopsOnlyAfterLastSessionClientLeaves(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	runtime := &presenceTestRuntime{
		testRuntime: newTestRuntime(id), cancelled: make(chan uuid.UUID, 2),
	}
	router := &presenceTestRouter{
		testTurnRouter: &testTurnRouter{runtime: runtime.testRuntime},
		cancelled:      make(chan uuid.UUID, 2),
	}
	instance, err := newTestServer(Dependencies{
		Runtime: runtime, Turns: router, APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)
	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)
	first := dialWebSocket(t, httpServer.URL, &id)
	second := dialWebSocket(t, httpServer.URL, &id)
	waitForWebSocketClients(t, instance, id, 2)
	require.NoError(t, first.Close())
	waitForWebSocketClients(t, instance, id, 1)
	assert.Empty(t, runtime.cancelled)
	assert.Empty(t, router.cancelled)
	require.NoError(t, second.Close())
	require.Eventually(t, func() bool {
		return len(runtime.cancelled) == 1 && len(router.cancelled) == 1
	}, time.Second, webSocketClientPollInterval)
	assert.Equal(t, id, <-runtime.cancelled)
	assert.Equal(t, id, <-router.cancelled)
	reconnected := dialWebSocket(t, httpServer.URL, &id)
	defer func() { require.NoError(t, reconnected.Close()) }()
	waitForWebSocketClients(t, instance, id, 1)
	assert.Empty(t, runtime.cancelled)
	assert.Empty(t, router.cancelled)
}
