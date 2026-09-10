package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every case here is rejected by the OpenAPI document alone. None of them has
// a hand-written check in any handler, and several were reachable before the
// validator existed: an out-of-range limit, a negative offset, a wrong-typed
// query value, and a body field that violates its declared minimum length all
// used to reach a handler or the runtime.
//
// The point of this test is that adding an endpoint to the spec now buys its
// input validation, instead of each handler having to remember.
func TestSpecValidatorRejectsWhatTheDocumentForbids(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "limit above the documented maximum",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?limit=9999",
		},
		{
			name:   "limit below the documented minimum",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?limit=0",
		},
		{
			name:   "negative offset",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?offset=-1",
		},
		{
			name:   "non-numeric limit",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?limit=lots",
		},
		{
			name:   "unknown order value",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?order=sideways",
		},
		{
			name:   "empty message violates minLength",
			method: http.MethodPost,
			path:   apiBaseURL + "/messages",
			body:   `{"message":""}`,
		},
		{
			name:   "unknown body field",
			method: http.MethodPost,
			path:   apiBaseURL + "/messages",
			body:   `{"message":"hi","nope":true}`,
		},
		{
			name:   "wrong body field type",
			method: http.MethodPost,
			path:   apiBaseURL + "/messages",
			body:   `{"message":42}`,
		},
		{
			name:   "event type below minLength",
			method: http.MethodPost,
			path:   apiBaseURL + "/session/events",
			body:   `{"type":"","summary":"x"}`,
		},
		{
			name:   "missing required event summary",
			method: http.MethodPost,
			path:   apiBaseURL + "/session/events",
			body:   `{"type":"app.error"}`,
		},
		{
			name:   "unknown event delivery mode",
			method: http.MethodPost,
			path:   apiBaseURL + "/session/events",
			body:   `{"type":"app.error","summary":"x","delivery":"telepathy"}`,
		},
		{
			name:   "unknown job state filter",
			method: http.MethodGet,
			path:   apiBaseURL + "/session/jobs?state=exploded",
		},
		{
			name:   "unknown job output stream",
			method: http.MethodGet,
			path: apiBaseURL + "/session/jobs/" + uuid.New().String() +
				"/output?stream=sideways",
		},
		{
			name:   "unknown agent run state filter",
			method: http.MethodGet,
			path:   apiBaseURL + "/session/agents?state=ascended",
		},
		{
			name:   "malformed uuid in the path",
			method: http.MethodGet,
			path:   apiBaseURL + "/session/jobs/not-a-uuid/output",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{
				Runtime: &testRuntime{sessionID: sessionID},
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				tc.method,
				tc.path,
				strings.NewReader(tc.body),
			)
			request.Header.Set(headerSessionID, sessionID.String())
			request.Header.Set(headerContentType, mediaTypeJSON)

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assertErrorCode(
				t,
				recorder,
				aichteeteapee.ErrorCodeValidationFailed,
			)
		})
	}
}

// The validator maps a rejection to 400, and the library's default would
// report BAD_REQUEST. Peen's documented contract says a malformed request
// shape is VALIDATION_FAILED, so the envelope must not drift just because the
// check moved from a handler into the spec.
func TestSpecValidatorKeepsTheDocumentedErrorEnvelope(t *testing.T) {
	sessionID := uuid.New()

	instance, err := New(Dependencies{
		Runtime: &testRuntime{sessionID: sessionID},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/messages?limit=9999",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertErrorCode(t, recorder, aichteeteapee.ErrorCodeValidationFailed)
}

// The spec declares security as bearerAuth OR {}, so it cannot express "auth
// is required only when the deployment set a token". Validation must not take
// that decision away from the authenticate middleware.
func TestSpecValidatorLeavesAuthenticationToTheMiddleware(t *testing.T) {
	sessionID := uuid.New()

	t.Run("no token configured accepts an unauthenticated request", func(t *testing.T) {
		instance, err := New(Dependencies{
			Runtime: &testRuntime{sessionID: sessionID},
		})
		require.NoError(t, err)

		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			apiBaseURL+"/messages",
			strings.NewReader(`{"message":"hello"}`),
		)
		request.Header.Set(headerContentType, mediaTypeJSON)

		recorder := httptest.NewRecorder()
		instance.testHandler.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusOK, recorder.Code)
	})

	t.Run("token configured still rejects a missing one", func(t *testing.T) {
		instance, err := New(Dependencies{
			Runtime:  &testRuntime{sessionID: sessionID},
			APIToken: testBearerToken,
		})
		require.NoError(t, err)

		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			apiBaseURL+"/messages",
			strings.NewReader(`{"message":"hello"}`),
		)
		request.Header.Set(headerContentType, mediaTypeJSON)

		recorder := httptest.NewRecorder()
		instance.testHandler.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assertUnauthorizedEnvelope(t, recorder)
	})
}
