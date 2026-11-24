package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIMountAuthenticatesBeforeProtocolAndKeepsEnvelope(t *testing.T) {
	t.Parallel()

	calls := 0
	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(uuid.New()), APIToken: testAPIToken,
		OpenAIHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			assert.Equal(t, "/chat/completions", r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
		}),
	})
	require.NoError(t, err)
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			openAIBaseURL+"/chat/completions", nil)
		request.Header.Set(headerAccept, "text/event-stream")
		if authenticated {
			request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)
		}
		response := httptest.NewRecorder()
		instance.inProcessHandler.ServeHTTP(response, request)
		if authenticated {
			assert.Equal(t, http.StatusAccepted, response.Code)

			continue
		}
		assert.Zero(t, calls)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
		assert.Contains(t, response.Body.String(), `"error":`)
		assert.NotContains(t, response.Body.String(), testAPIToken)
	}
	assert.Equal(t, 1, calls)
}

func TestDisabledOpenAISurfaceDoesNotFallThroughToTheSPA(t *testing.T) {
	t.Parallel()

	instance, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)
	for _, suffix := range []string{"", "/models", "/chat/completions"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			openAIBaseURL+suffix, nil)
		response := httptest.NewRecorder()
		instance.inProcessHandler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code)
	}
}
