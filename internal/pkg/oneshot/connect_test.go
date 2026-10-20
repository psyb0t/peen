package oneshot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const startupFailureTestTimeout = 5 * time.Second

type fakeRunnable func(context.Context) error

func (run fakeRunnable) Run(ctx context.Context) error {
	return run(ctx)
}

func TestStartInProcessReportsStartupFailure(t *testing.T) {
	// The endpoint is process-wide, so these lifecycle tests stay serial.
	ctx, cancel := context.WithTimeout(t.Context(), startupFailureTestTimeout)
	defer cancel()
	_, err := startInProcess(ctx, fakeRunnable(func(context.Context) error {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied, "startup refused")
	}), "")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	require.NoError(t, ctx.Err(), "startup failure waited for the timeout")
	assert.Nil(t, control.SharedInProcessEndpoint().Listener())
}

func TestStartInProcessCloseWaitsForController(t *testing.T) {
	const token = "EXAMPLE-DO-NOT-USE"

	stopStarted, allowStop, stopped := make(chan struct{}),
		make(chan struct{}), make(chan struct{})
	controller := fakeRunnable(func(ctx context.Context) error {
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ready" {
					w.WriteHeader(http.StatusOK)

					return
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(http.StatusUnauthorized)

					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"items":[]}`)
				assert.NoError(t, err)
			}),
			ReadHeaderTimeout: oneshotTestTimeout,
		}
		serving := make(chan error, 1)
		go func() {
			serving <- server.Serve(control.SharedInProcessEndpoint().Listener())
		}()
		<-ctx.Done()
		close(stopStarted)
		<-allowStop
		closeErr := server.Close()
		serveErr := <-serving
		close(stopped)
		if closeErr != nil {
			return ctxerrors.Wrap(closeErr, "close test controller")
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return ctxerrors.Wrap(serveErr, "serve test controller")
		}

		return nil
	})
	connection, err := startInProcess(t.Context(), controller, token)
	require.NoError(t, err)
	t.Cleanup(func() {
		select {
		case <-allowStop:
		default:
			close(allowStop)
		}
		assert.NoError(t, connection.Close(context.WithoutCancel(t.Context())))
	})
	require.True(t, connection.Client.Reachable(t.Context()))
	_, err = connection.Client.ListSessions(t.Context())
	require.NoError(t, err, "in-process REST calls must authenticate")
	closed := make(chan error, 1)
	go func() { closed <- connection.Close(t.Context()) }()
	waitOneshot(t, stopStarted)
	select {
	case err := <-closed:
		t.Fatalf("close returned while controller was running: %v", err)
	default:
	}
	close(allowStop)
	require.NoError(t, <-closed)
	waitOneshot(t, stopped)
	assert.Nil(t, control.SharedInProcessEndpoint().Listener())
}
