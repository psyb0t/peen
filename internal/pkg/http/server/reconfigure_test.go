package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	reconfigurePath      = apiBaseURL + "/session/reconfigure"
	profileDecisionsPath = apiBaseURL + "/session/profile-decisions"
)

func reconfigureRequest(t *testing.T, sessionID uuid.UUID, body string) *http.Request {
	t.Helper()

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		reconfigurePath,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(headerSessionID, sessionID.String())

	return request
}

// A reconfiguration passes the client's named profile and reason through
// unchanged, and answers with the recorded decision.
func TestServerReconfigureSessionRecordsTheDecision(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	fromProfile := "native"

	registry := &testSessionRegistry{
		decision: api.SessionProfileDecision{
			Id:          uuid.New(),
			SessionId:   sessionID,
			FromProfile: &fromProfile,
			ToProfile:   "docker.sandbox",
			Reason:      "moving to an isolated environment",
			DecidedAt:   time.Now().UTC(),
		},
	}

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, reconfigureRequest(
		t,
		sessionID,
		`{"profile":"docker.sandbox","reason":"moving to an isolated environment"}`,
	))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	decision := api.SessionProfileDecision{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &decision))
	assert.Equal(t, sessionID, decision.SessionId)
	assert.Equal(t, "docker.sandbox", decision.ToProfile)
	require.NotNil(t, decision.FromProfile)
	assert.Equal(t, "native", *decision.FromProfile)

	require.Len(t, registry.reconfigured, 1)
	assert.Equal(t, "docker.sandbox", registry.reconfigured[0].Profile)
	assert.Equal(
		t,
		"moving to an isolated environment",
		registry.reconfigured[0].Reason,
	)
}

// A profile the deployment does not define is refused, and the response names
// no profile, so a client cannot enumerate the allowed set by probing.
func TestServerReconfigureSessionRefusesAnUndefinedProfile(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionErr: ctxerrors.Wrapf(
				commerr.ErrPermissionDenied,
				"execution profile %q is not allowed by this deployment",
				"docker.host-like",
			),
		},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, reconfigureRequest(
		t,
		sessionID,
		`{"profile":"docker.host-like","reason":"needs the host"}`,
	))

	require.Equal(t, http.StatusForbidden, recorder.Code)

	envelope := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, ErrorCodeExecutionProfileNotAllowed, envelope.Code)
	assert.NotContains(t, envelope.Message, "docker.host-like")
}

// A session with a turn in flight is a conflict rather than a bad request: the
// same call succeeds once the turn ends.
func TestServerReconfigureSessionRefusesABusySession(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionErr: ctxerrors.Wrap(
				session.ErrSessionBusy,
				"a session with a running turn cannot be reconfigured",
			),
		},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, reconfigureRequest(
		t,
		sessionID,
		`{"profile":"native","reason":"back to native"}`,
	))

	require.Equal(t, http.StatusConflict, recorder.Code)

	envelope := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, ErrorCodeSessionBusy, envelope.Code)
}

func TestServerReconfigureSessionRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	testCases := []struct {
		name      string
		body      string
		setHeader bool
	}{
		{
			name:      "missing profile",
			body:      `{"reason":"why"}`,
			setHeader: true,
		},
		{
			name:      "missing reason",
			body:      `{"profile":"native"}`,
			setHeader: true,
		},
		{
			name:      "blank profile",
			body:      `{"profile":"","reason":"why"}`,
			setHeader: true,
		},
		{
			name:      "unknown field",
			body:      `{"profile":"native","reason":"why","image":"evil:latest"}`,
			setHeader: true,
		},
		{
			name: "missing session header",
			body: `{"profile":"native","reason":"why"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := &testSessionRegistry{}

			instance, err := newTestServer(Dependencies{
				Runtime:  newTestRuntime(sessionID),
				Sessions: registry,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				reconfigurePath,
				strings.NewReader(tc.body),
			)
			request.Header.Set("Content-Type", "application/json")

			if tc.setHeader {
				request.Header.Set(headerSessionID, sessionID.String())
			}

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(
				t,
				http.StatusBadRequest,
				recorder.Code,
				recorder.Body.String(),
			)
			assert.Empty(
				t,
				registry.reconfigured,
				"a malformed request must not reach the registry",
			)
		})
	}
}

// The decision history is addressed by the session header, so one session
// cannot read another's profile changes.
func TestServerListSessionProfileDecisionsIsSessionScoped(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	decisionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionPage: api.SessionProfileDecisionPage{
				Items: []api.SessionProfileDecision{{
					Id:        decisionID,
					SessionId: sessionID,
					ToProfile: "docker.sandbox",
					Reason:    "isolating the workspace",
					DecidedAt: time.Now().UTC(),
				}},
				Limit: 50,
			},
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		profileDecisionsPath,
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	page := api.SessionProfileDecisionPage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, decisionID, page.Items[0].Id)
	assert.Equal(t, sessionID, page.Items[0].SessionId)
}

func TestServerListSessionProfileDecisionsReportsAnUnknownSession(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionListErr: ctxerrors.Wrap(commerr.ErrNotFound, "session"),
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		profileDecisionsPath,
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNotFound, recorder.Code)

	envelope := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, ErrorCodeSessionNotFound, envelope.Code)
}
