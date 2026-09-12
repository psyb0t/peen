package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	eventsPath        = apiBaseURL + "/session/events"
	noticesPath       = apiBaseURL + "/session/notices"
	validEventBody    = `{"type":"app.error","summary":"checkout returned 500"}`
	reservedEventBody = `{"type":"job.exited","summary":"forged"}`
	testBearerToken   = "test-token"
)

func TestServerSessionEventEndpoints(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists durable protocol events",
			method:     http.MethodGet,
			path:       eventsPath,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:   "lists an unknown session",
			method: http.MethodGet,
			path:   eventsPath,
			runtime: &testRuntime{
				sessionID: sessionID,
				eventsErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:       "publishes a notice",
			method:     http.MethodPost,
			path:       noticesPath,
			body:       validEventBody,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "rejects an empty body",
			method:     http.MethodPost,
			path:       noticesPath,
			body:       "",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "rejects an unknown field",
			method:     http.MethodPost,
			path:       noticesPath,
			body:       `{"type":"app.error","summary":"x","nope":1}`,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:   "maps a reserved type to a client error",
			method: http.MethodPost,
			path:   noticesPath,
			body:   reservedEventBody,
			runtime: &testRuntime{
				sessionID: sessionID,
				eventsErr: events.ErrReservedType,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:   "maps a malformed type to a client error",
			method: http.MethodPost,
			path:   noticesPath,
			body:   `{"type":"Nope","summary":"x"}`,
			runtime: &testRuntime{
				sessionID: sessionID,
				eventsErr: events.ErrInvalidType,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:   "publishes a notice to an unknown session",
			method: http.MethodPost,
			path:   noticesPath,
			body:   validEventBody,
			runtime: &testRuntime{
				sessionID: sessionID,
				eventsErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{Runtime: tc.runtime})
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

			assert.Equal(t, tc.wantStatus, recorder.Code)

			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

// The event endpoints must sit behind the same bearer check as everything
// else. Auth is server-wide middleware, so this proves the new routes did not
// somehow land outside it.
func TestServerSessionEventEndpointsRequireTheBearerToken(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list events", method: http.MethodGet, path: eventsPath},
		{
			name:   "publish notice",
			method: http.MethodPost,
			path:   noticesPath,
			body:   validEventBody,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{
				Runtime:  &testRuntime{sessionID: sessionID},
				APIToken: testBearerToken,
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

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assertUnauthorizedEnvelope(t, recorder)
		})
	}
}

func TestServerSessionEventEndpointsRejectAMissingSessionHeader(t *testing.T) {
	instance, err := New(Dependencies{Runtime: &testRuntime{}})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		noticesPath,
		strings.NewReader(validEventBody),
	)
	request.Header.Set(headerContentType, mediaTypeJSON)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
