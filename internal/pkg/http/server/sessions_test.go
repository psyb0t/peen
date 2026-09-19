package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWorkspacePath = "/srv/work/project"

func testOpenedSession(sessionID uuid.UUID, created bool) api.OpenedSession {
	now := time.Now().UTC()

	return api.OpenedSession{
		Created: created,
		Session: api.Session{
			Id:        sessionID,
			CreatedAt: now,
			UpdatedAt: now,
			Agent:     "default",
			Model:     "test/model",
			Workspace: testWorkspacePath,
		},
	}
}

func openSessionRequest(t *testing.T, body string) *http.Request {
	t.Helper()

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/sessions/open",
		strings.NewReader(body),
	)
	request.Header.Set(headerContentType, mediaTypeJSON)

	return request
}

// Opening a workspace is the only way to get a session, so it must work
// without the caller already knowing a session ID.
func TestServerOpenSessionReturnsTheSession(t *testing.T) {
	sessionID := uuid.New()
	registry := &testSessionRegistry{
		opened: testOpenedSession(sessionID, true),
	}

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(
		recorder,
		openSessionRequest(t, `{"workspace":"`+testWorkspacePath+`"}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var body api.OpenedSession
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	assert.True(t, body.Created)
	assert.Equal(t, sessionID, body.Session.Id)
	assert.Equal(t, testWorkspacePath, body.Session.Workspace)
	assert.Equal(t, sessionID.String(), recorder.Header().Get(headerSessionID))
	assert.Equal(t, []string{testWorkspacePath}, registry.requests)
}

// Naming a profile is the only environment choice a client makes, so the name it
// sends must reach the registry unchanged.
func TestServerOpenSessionForwardsTheNamedProfile(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	registry := &testSessionRegistry{
		opened: testOpenedSession(sessionID, true),
	}

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(
		recorder,
		openSessionRequest(
			t,
			`{"workspace":"`+testWorkspacePath+
				`","profile":"docker.sandbox"}`,
		),
	)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, []string{"docker.sandbox"}, registry.profiles)
}

// A request that names no profile forwards an empty name, which the registry
// reads as the deployment default rather than a refusal.
func TestServerOpenSessionForwardsAnEmptyProfile(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	registry := &testSessionRegistry{
		opened: testOpenedSession(sessionID, true),
	}

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(
		recorder,
		openSessionRequest(t, `{"workspace":"`+testWorkspacePath+`"}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, []string{""}, registry.profiles)
}

// Resuming reports created=false, which is how a client tells a new workspace
// from one it already opened.
func TestServerOpenSessionReportsResume(t *testing.T) {
	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			opened: testOpenedSession(sessionID, false),
		},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(
		recorder,
		openSessionRequest(t, `{"workspace":"`+testWorkspacePath+`"}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var body api.OpenedSession
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.False(t, body.Created)
}

func TestServerOpenSessionMapsRefusals(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name       string
		body       string
		openErr    error
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name: "workspace outside every configured root",
			body: `{"workspace":"` + testWorkspacePath + `"}`,
			openErr: ctxerrors.Wrap(
				commerr.ErrPermissionDenied,
				"outside every configured workspace root",
			),
			wantStatus: http.StatusForbidden,
			wantCode:   ErrorCodeWorkspaceNotAllowed,
		},
		{
			name: "workspace is not a directory",
			body: `{"workspace":"` + testWorkspacePath + `"}`,
			openErr: ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"workspace is not a directory",
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "missing workspace field",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank workspace field",
			body:       `{"workspace":""}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := newTestServer(Dependencies{
				Runtime:  newTestRuntime(sessionID),
				Sessions: &testSessionRegistry{openErr: tc.openErr},
			})
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(
				recorder,
				openSessionRequest(t, tc.body),
			)

			assert.Equal(
				t,
				tc.wantStatus,
				recorder.Code,
				recorder.Body.String(),
			)

			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

// Listing takes no session header, because it is how a client discovers which
// sessions exist.
func TestServerListSessionsNeedsNoSessionHeader(t *testing.T) {
	sessionID := uuid.New()
	opened := testOpenedSession(sessionID, false)

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			page: api.SessionPage{
				Items:   []api.Session{opened.Session},
				Limit:   50,
				Offset:  0,
				HasMore: false,
			},
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/sessions",
		nil,
	)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var page api.SessionPage
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, sessionID, page.Items[0].Id)
	assert.False(t, page.HasMore)
}

// Opening a workspace creates durable state and reports a host path, so it
// sits behind the bearer token. The registry must not be reached at all.
func TestServerSessionControlEndpointsRequireBearerToken(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name    string
		method  string
		path    string
		body    string
		hasBody bool
	}{
		{
			name:    "open a workspace",
			method:  http.MethodPost,
			path:    apiBaseURL + "/sessions/open",
			body:    `{"workspace":"` + testWorkspacePath + `"}`,
			hasBody: true,
		},
		{
			name:   "list sessions",
			method: http.MethodGet,
			path:   apiBaseURL + "/sessions",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := &testSessionRegistry{
				opened: testOpenedSession(sessionID, true),
			}

			instance, err := newTestServer(Dependencies{
				Runtime:  newTestRuntime(sessionID),
				Sessions: registry,
				APIToken: testBearerToken,
			})
			require.NoError(t, err)

			var body io.Reader
			if tc.hasBody {
				body = strings.NewReader(tc.body)
			}

			request := httptest.NewRequestWithContext(
				t.Context(),
				tc.method,
				tc.path,
				body,
			)
			if tc.hasBody {
				request.Header.Set(headerContentType, mediaTypeJSON)
			}

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assertUnauthorizedEnvelope(t, recorder)
			assert.Empty(
				t,
				registry.requests,
				"an unauthenticated request must not reach the registry",
			)
		})
	}
}

// The worker and profile reads expose deployment layout and container
// identity, so they sit behind the bearer token like every other endpoint.
func TestServerWorkerReadsRequireBearerToken(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name      string
		path      string
		setHeader bool
	}{
		{
			name: "execution profiles",
			path: apiBaseURL + "/execution-profiles",
		},
		{
			name:      "session workers",
			path:      apiBaseURL + "/session/workers",
			setHeader: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry := &testSessionRegistry{
				opened: testOpenedSession(sessionID, true),
			}

			instance, err := newTestServer(Dependencies{
				Runtime:  newTestRuntime(sessionID),
				Sessions: registry,
				APIToken: testBearerToken,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				tc.path,
				nil,
			)
			if tc.setHeader {
				request.Header.Set(headerSessionID, sessionID.String())
			}

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assertUnauthorizedEnvelope(t, recorder)
		})
	}
}

// A session-scoped worker read is addressed by the session header, so one
// session cannot read another's generations.
func TestServerListSessionWorkersIsSessionScoped(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	generationID := uuid.New()

	registry := &testSessionRegistry{
		workerPage: api.WorkerGenerationPage{
			Items: []api.WorkerGeneration{{
				Id:        generationID,
				SessionId: sessionID,
				Kind:      api.WorkerGenerationKindNative,
				Profile:   "native",
				State:     api.WorkerGenerationStateReady,
				Workspace: testWorkspacePath,
			}},
			Limit: 50,
		},
	}

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session/workers",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	page := api.WorkerGenerationPage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, sessionID, page.Items[0].SessionId)
	assert.Equal(t, generationID, page.Items[0].Id)
}

// A missing session header is rejected before the registry is reached.
func TestServerListSessionWorkersRejectsAMissingSessionHeader(t *testing.T) {
	t.Parallel()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(uuid.New()),
		Sessions: &testSessionRegistry{},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session/workers",
		nil,
	)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestServerListSessionsRejectsAnInvalidPage(t *testing.T) {
	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(sessionID),
		Sessions: &testSessionRegistry{
			listErr: ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"session page limit is out of range",
			),
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/sessions?limit=201",
		nil,
	)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertErrorCode(t, recorder, aichteeteapee.ErrorCodeValidationFailed)
}
