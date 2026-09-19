package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testToken        = "test-token"
	testWorkspace    = "/srv/work/project"
	testReadyTimeout = 2 * time.Second
)

// newTestClient points a client at a stub controller.
func newTestClient(t *testing.T, handler http.Handler) *controlclient.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		Token:         testToken,
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)

	return client
}

func TestNewRejectsAMissingAddress(t *testing.T) {
	t.Parallel()

	client, err := controlclient.New(controlclient.Options{})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	assert.Nil(t, client)
}

// Discovery is what tells a command whether a controller already runs.
func TestReachableReportsWhetherTheControllerAnswers(t *testing.T) {
	t.Parallel()

	t.Run("answering", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
		))

		assert.True(t, client.Reachable(t.Context()))
	})

	t.Run("not answering", func(t *testing.T) {
		t.Parallel()

		client, err := controlclient.New(controlclient.Options{
			// Port zero is never listening, so this is a closed endpoint
			// rather than a slow one.
			ListenAddress: "127.0.0.1:0",
			HTTPClient:    &http.Client{Timeout: testReadyTimeout},
		})
		require.NoError(t, err)

		assert.False(t, client.Reachable(t.Context()))
	})
}

// The command sends the bearer token the deployment configured, so a
// token-protected controller accepts it.
func TestOpenSessionSendsTheConfiguredToken(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	var gotAuthorization string

	var gotBody string

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuthorization = r.Header.Get("Authorization")

			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			gotBody = string(body)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(
				`{"created":true,"session":{"id":"` + sessionID.String() +
					`","createdAt":"2026-01-01T00:00:00Z",` +
					`"updatedAt":"2026-01-01T00:00:00Z","messageCount":0,` +
					`"completedTurnCount":0,"activeTurn":false,` +
					`"agent":"default","model":"m","workspace":"` +
					testWorkspace + `"}}`,
			))
		},
	))

	opened, err := client.OpenSession(t.Context(), testWorkspace)
	require.NoError(t, err)

	assert.Equal(t, "Bearer "+testToken, gotAuthorization)
	assert.Contains(t, gotBody, testWorkspace)
	assert.True(t, opened.Created)
	assert.Equal(t, sessionID, opened.Session.Id)
}

// A refusal has to reach the operator as the controller's own stable code, not
// a bare status number.
func TestOpenSessionReportsTheControllerErrorEnvelope(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(
				`{"code":"WORKSPACE_NOT_ALLOWED","message":"outside roots"}`,
			))
		},
	))

	_, err := client.OpenSession(t.Context(), testWorkspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WORKSPACE_NOT_ALLOWED")
	assert.Contains(t, err.Error(), "outside roots")
}

// Stopping a session addresses one session through the session header, and
// reports whether a turn was actually running.
func TestCancelSessionAddressesTheNamedSession(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	var gotSessionID string

	var gotPath string

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotSessionID = r.Header.Get("X-Session-ID")
			gotPath = r.URL.Path

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"cancelRequested":true}`))
		},
	))

	result, err := client.CancelSession(t.Context(), sessionID)
	require.NoError(t, err)

	assert.Equal(t, sessionID.String(), gotSessionID)
	assert.Equal(t, "/v1/session/cancel", gotPath)
	assert.True(t, result.CancelRequested)
}

// An idle session has nothing to cancel. That is a reported outcome, not an
// error, so a command can say so plainly.
func TestCancelSessionReportsAnIdleSession(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"cancelRequested":false}`))
		},
	))

	result, err := client.CancelSession(t.Context(), uuid.New())
	require.NoError(t, err)
	assert.False(t, result.CancelRequested)
}

// An unknown session must surface the controller's own code.
func TestCancelSessionReportsAnUnknownSession(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(
				`{"code":"SESSION_NOT_FOUND","message":"session not found"}`,
			))
		},
	))

	_, err := client.CancelSession(t.Context(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SESSION_NOT_FOUND")
}

// A reachable controller is used as it is. Starting a second one would be a
// duplicate supervisor.
func TestEnsureRunningDoesNotStartAReachableController(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))

	started := false

	require.NoError(t, client.EnsureRunning(
		t.Context(),
		controlclient.LaunchOptions{
			StateDirectory: t.TempDir(),
			ReadyTimeout:   testReadyTimeout,
			StartController: func(context.Context, string) error {
				started = true

				return nil
			},
		},
	))

	assert.False(t, started, "a reachable controller must not be started again")
}

// When nothing is listening the client starts one, then waits for it to answer.
func TestEnsureRunningStartsAndWaitsForReadiness(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex

	listening := false

	client := newTestClient(t, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			mutex.Lock()
			defer mutex.Unlock()

			if !listening {
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}

			w.WriteHeader(http.StatusOK)
		},
	))

	startCalls := 0

	require.NoError(t, client.EnsureRunning(
		t.Context(),
		controlclient.LaunchOptions{
			StateDirectory: t.TempDir(),
			ReadyTimeout:   testReadyTimeout,
			StartController: func(context.Context, string) error {
				startCalls++

				mutex.Lock()
				defer mutex.Unlock()

				listening = true

				return nil
			},
		},
	))

	assert.Equal(t, 1, startCalls)
}

// The lock is what keeps two racing clients from starting two controllers. The
// loser re-checks under the lock and finds the winner's controller.
func TestEnsureRunningStartsOneControllerUnderContention(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex

	listening := false
	startCalls := 0

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()

		if !listening {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	stateDirectory := t.TempDir()

	start := func(context.Context, string) error {
		mutex.Lock()
		defer mutex.Unlock()

		startCalls++
		listening = true

		return nil
	}

	const racers = 4

	var group sync.WaitGroup

	group.Add(racers)

	for range racers {
		go func() {
			defer group.Done()

			client, err := controlclient.New(controlclient.Options{
				ListenAddress: strings.TrimPrefix(server.URL, "http://"),
				HTTPClient:    server.Client(),
			})
			if err != nil {
				return
			}

			_ = client.EnsureRunning(
				context.Background(),
				controlclient.LaunchOptions{
					StateDirectory:  stateDirectory,
					ReadyTimeout:    testReadyTimeout,
					StartController: start,
				},
			)
		}()
	}

	group.Wait()

	mutex.Lock()
	defer mutex.Unlock()

	assert.Equal(
		t,
		1,
		startCalls,
		"the start lock must let only one client start a controller",
	)
}

func TestEnsureRunningRequiresAStateDirectory(t *testing.T) {
	t.Parallel()

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: "127.0.0.1:0",
		HTTPClient:    &http.Client{Timeout: testReadyTimeout},
	})
	require.NoError(t, err)

	err = client.EnsureRunning(t.Context(), controlclient.LaunchOptions{
		ReadyTimeout: testReadyTimeout,
	})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
