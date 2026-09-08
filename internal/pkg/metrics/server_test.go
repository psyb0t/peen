package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerRequiresMetrics(t *testing.T) {
	t.Parallel()

	_, err := NewServer(nil)

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestHandlerServesMetricsOnlyAtMetricsPath(t *testing.T) {
	t.Parallel()

	instance := New()
	metricsRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/metrics",
		nil,
	)
	metricsRecorder := httptest.NewRecorder()
	instance.Handler().ServeHTTP(metricsRecorder, metricsRequest)

	assert.Equal(t, http.StatusOK, metricsRecorder.Code)
	assert.Contains(t, metricsRecorder.Body.String(), "peen_build_info")

	notFoundRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/v1/messages",
		nil,
	)
	notFoundRecorder := httptest.NewRecorder()
	instance.Handler().ServeHTTP(notFoundRecorder, notFoundRequest)

	assert.Equal(t, http.StatusNotFound, notFoundRecorder.Code)
}

func TestServerServeListenerRejectsNilListener(t *testing.T) {
	t.Parallel()

	instance, err := NewServer(New())
	require.NoError(t, err)

	err = instance.ServeListener(context.Background(), nil)

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
