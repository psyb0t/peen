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
	reconfigurePath         = apiBaseURL + "/session/reconfigure"
	agentSpaceDecisionsPath = apiBaseURL + "/session/agent-space-decisions"
	removedDecisionsPath    = apiBaseURL + "/session/profile-decisions"
	agentSpacesPath         = apiBaseURL + "/agent-spaces"
	removedSpacesPath       = apiBaseURL + "/execution-profiles"

	testSealedSpace = "docker:sandbox"
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

// A reconfiguration passes the client's named agent space and reason through
// unchanged, and answers with the recorded decision.
func TestServerReconfigureSessionRecordsTheDecision(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	fromAgentSpace := "control"

	registry := &testSessionRegistry{
		decision: api.SessionAgentSpaceDecision{
			Id:             uuid.New(),
			SessionId:      sessionID,
			FromAgentSpace: &fromAgentSpace,
			ToAgentSpace:   testSealedSpace,
			Reason:         "moving to an isolated environment",
			DecidedAt:      time.Now().UTC(),
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
		`{"agentSpace":"docker:sandbox","reason":"moving to an isolated environment"}`,
	))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	decision := api.SessionAgentSpaceDecision{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &decision))
	assert.Equal(t, sessionID, decision.SessionId)
	assert.Equal(t, testSealedSpace, decision.ToAgentSpace)
	require.NotNil(t, decision.FromAgentSpace)
	assert.Equal(t, "control", *decision.FromAgentSpace)

	require.Len(t, registry.reconfigured, 1)
	assert.Equal(t, testSealedSpace, registry.reconfigured[0].AgentSpace)
	assert.Equal(
		t,
		"moving to an isolated environment",
		registry.reconfigured[0].Reason,
	)
}

// An agent space the deployment does not offer is refused, and the response
// names no agent space, so a client cannot enumerate the allowed set by
// probing.
func TestServerReconfigureSessionRefusesAnUndefinedAgentSpace(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionErr: ctxerrors.Wrapf(
				commerr.ErrPermissionDenied,
				"agent space %q is not allowed by this deployment",
				"docker:ops",
			),
		},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, reconfigureRequest(
		t,
		sessionID,
		`{"agentSpace":"docker:ops","reason":"needs the host"}`,
	))

	require.Equal(t, http.StatusForbidden, recorder.Code)

	envelope := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, ErrorCodeAgentSpaceNotAllowed, envelope.Code)
	assert.Equal(t, "AGENT_SPACE_NOT_ALLOWED", envelope.Code)
	assert.NotContains(t, envelope.Message, "docker:ops")
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
		`{"agentSpace":"control","reason":"back to control"}`,
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
			name:      "missing agent space",
			body:      `{"reason":"why"}`,
			setHeader: true,
		},
		{
			name:      "missing reason",
			body:      `{"agentSpace":"control"}`,
			setHeader: true,
		},
		{
			name:      "blank agent space",
			body:      `{"agentSpace":"","reason":"why"}`,
			setHeader: true,
		},
		{
			name:      "the removed profile field",
			body:      `{"profile":"control","reason":"why"}`,
			setHeader: true,
		},
		{
			name:      "unknown field",
			body:      `{"agentSpace":"control","reason":"why","image":"evil:latest"}`,
			setHeader: true,
		},
		{
			name: "missing session header",
			body: `{"agentSpace":"control","reason":"why"}`,
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
// cannot read another's agent space changes.
func TestServerListSessionAgentSpaceDecisionsIsSessionScoped(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	decisionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			decisionPage: api.SessionAgentSpaceDecisionPage{
				Items: []api.SessionAgentSpaceDecision{{
					Id:           decisionID,
					SessionId:    sessionID,
					ToAgentSpace: testSealedSpace,
					Reason:       "isolating the workspace",
					DecidedAt:    time.Now().UTC(),
				}},
				Limit: 50,
			},
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		agentSpaceDecisionsPath,
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	page := api.SessionAgentSpaceDecisionPage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, decisionID, page.Items[0].Id)
	assert.Equal(t, sessionID, page.Items[0].SessionId)
}

func TestServerListSessionAgentSpaceDecisionsReportsAnUnknownSession(
	t *testing.T,
) {
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
		agentSpaceDecisionsPath,
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

// The agent space list reaches the client with each Docker space's grants and
// the warnings for its risky ones.
func TestServerListAgentSpacesReportsTheGrants(t *testing.T) {
	t.Parallel()

	profile := "ops"
	docker := api.AgentSpaceDockerFull
	network := api.AgentSpaceNetworkHost
	sudo := true

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(uuid.New()),
		Sessions: &testSessionRegistry{
			agentSpaceList: api.AgentSpaceList{
				Default: "docker:default",
				Items: []api.AgentSpace{{
					Name:               "docker:ops",
					Kind:               api.AgentSpaceKindDocker,
					Profile:            &profile,
					Docker:             &docker,
					Network:            &network,
					Sudo:               &sudo,
					HostRootEquivalent: true,
					CapabilityWarnings: []string{"docker: full is host root"},
				}},
			},
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		agentSpacesPath,
		nil,
	)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	list := api.AgentSpaceList{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &list))
	assert.Equal(t, "docker:default", list.Default)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "docker:ops", list.Items[0].Name)
	require.NotNil(t, list.Items[0].Docker)
	assert.Equal(t, api.AgentSpaceDockerFull, *list.Items[0].Docker)
	require.NotNil(t, list.Items[0].Network)
	assert.Equal(t, api.AgentSpaceNetworkHost, *list.Items[0].Network)
	assert.True(t, list.Items[0].HostRootEquivalent)
	assert.Len(t, list.Items[0].CapabilityWarnings, 1)
}

// The old execution profile routes are gone, not aliased.
func TestServerDoesNotServeTheRemovedProfileRoutes(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	for _, path := range []string{removedSpacesPath, removedDecisionsPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			instance, err := newTestServer(Dependencies{
				Runtime:  newTestRuntime(sessionID),
				Sessions: &testSessionRegistry{},
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				path,
				nil,
			)
			request.Header.Set(headerSessionID, sessionID.String())

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusNotFound, recorder.Code)
		})
	}
}
