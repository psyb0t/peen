package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerBuildsItsSerbewrListener(t *testing.T) {
	instance, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)

	assert.NotNil(t, instance.httpServer)
}

func TestServerDoesNotServeHTTPMessageSubmission(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	instance, err := newTestServer(Dependencies{
		Runtime:  runtime,
		APIToken: testAPIToken,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/messages",
		nil,
	)
	request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)

	recorder := httptest.NewRecorder()

	instance.inProcessHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Zero(t, runtime.runCalls)
}

func TestServerDoesNotServePrivateMetricsFromThePublicListener(t *testing.T) {
	instance, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)

	for _, requestPath := range []string{privateMetricsPath, privateMetricsPath + "/"} {
		t.Run(requestPath, func(t *testing.T) {
			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				requestPath,
				nil,
			)
			recorder := httptest.NewRecorder()

			instance.inProcessHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusNotFound, recorder.Code)
		})
	}
}

// A malformed X-Session-ID header fails oapi-codegen's own UUID parsing
// before the request reaches a handler, distinct from a body shape
// rejection, so it carries its own documented code.
func TestServerRejectsMalformedSessionIDHeader(t *testing.T) {
	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(uuid.New()),
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session",
		nil,
	)
	request.Header.Set(headerSessionID, "not-a-uuid")

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertErrorCode(t, recorder, ErrorCodeInvalidSessionID)
}
