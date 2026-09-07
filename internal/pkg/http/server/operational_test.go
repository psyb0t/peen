package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A probe carries no credentials. If the bearer check applied to it, an
// orchestrator would mark a perfectly healthy deployment down the moment a
// token was configured.
func TestOperationalProbesAnswerWithoutCredentials(t *testing.T) {
	testCases := []struct {
		name string
		path string
	}{
		{name: "liveness", path: healthzPath},
		{name: "readiness", path: readyPath},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{
				Runtime:  &testRuntime{sessionID: uuid.New()},
				APIToken: testBearerToken,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				tc.path,
				nil,
			)
			recorder := httptest.NewRecorder()
			instance.echo.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)

			status := operationalStatus{}
			require.NoError(
				t,
				json.Unmarshal(recorder.Body.Bytes(), &status),
			)
			assert.Equal(t, operationalStatusOK, status.Status)
		})
	}
}

// The probes are not in the OpenAPI document, so the spec validator would
// reject them if they were not skipped.
func TestOperationalProbesBypassTheSpecValidator(t *testing.T) {
	instance, err := New(Dependencies{
		Runtime: &testRuntime{sessionID: uuid.New()},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		healthzPath,
		nil,
	)
	recorder := httptest.NewRecorder()
	instance.echo.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

// The agent API keeps its bearer check. Skipping is scoped to the probes and
// must not leak onto anything under /v1.
func TestSkipOperationalDoesNotWidenTheAgentAPI(t *testing.T) {
	sessionID := uuid.New()

	instance, err := New(Dependencies{
		Runtime:  &testRuntime{sessionID: sessionID},
		APIToken: testBearerToken,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.echo.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assertUnauthorizedEnvelope(t, recorder)
}

func TestIsOperationalPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		path string
		want bool
	}{
		{name: "liveness", path: healthzPath, want: true},
		{name: "readiness", path: readyPath, want: true},
		{name: "trailing slash", path: readyPath + "/", want: true},
		{name: "agent api", path: apiBaseURL + "/messages"},
		{name: "a prefix match is not enough", path: healthzPath + "x"},
		{name: "empty", path: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, isOperationalPath(tc.path))
		})
	}
}
