package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLongResponseExtendsOnlyItsRequestDeadline(t *testing.T) {
	t.Parallel()
	for _, extend := range []bool{false, true} {
		name := "ordinary response"
		if extend {
			name = "long response"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertResponsePastDeadline(t, extend)
		})
	}
}

func assertResponsePastDeadline(t *testing.T, extend bool) {
	t.Helper()
	const writeTimeout = 10 * time.Millisecond
	completed := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extend {
			if err := prepareLongResponse(w); err != nil {
				completed <- err

				return
			}
		}
		select {
		case <-time.After(5 * writeTimeout):
		case <-r.Context().Done():
		}
		_, err := io.WriteString(w, "completed")
		completed <- err
	})
	instance := &Server{deps: Dependencies{Metrics: metrics.New()}}
	listener := httptest.NewUnstartedServer(middleware.Chain(handler,
		globalMiddlewares(instance)...))
	listener.Config.WriteTimeout = writeTimeout
	listener.Start()
	t.Cleanup(listener.Close)
	client := listener.Client()
	client.Timeout = time.Second
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		listener.URL, nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	if !extend {
		require.Error(t, err)
		<-completed //nolint:errcheck // Only waits for the handler to end.

		return
	}
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, "completed", string(body))
	require.NoError(t, <-completed)
}

type deadlineFailureWriter struct {
	*httptest.ResponseRecorder
}

func (deadlineFailureWriter) SetWriteDeadline(time.Time) error {
	return commerr.ErrExecFailed
}

func TestLongResponseRetainsDeadlineErrors(t *testing.T) {
	t.Parallel()
	require.ErrorIs(t, prepareLongResponse(deadlineFailureWriter{
		ResponseRecorder: httptest.NewRecorder(),
	}), commerr.ErrExecFailed)
	require.NoError(t, prepareLongResponse(httptest.NewRecorder()))
}
