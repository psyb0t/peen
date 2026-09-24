package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workspaceRootsPath = apiBaseURL + "/workspace-roots"

func TestServerListWorkspaceRoots(t *testing.T) {
	t.Parallel()

	want := api.WorkspaceRootList{Roots: []string{
		"/work/catalog",
		"/work/platform",
	}}
	instance, err := newTestServer(Dependencies{
		APIToken: testAPIToken,
		Runtime:  newTestRuntime(uuid.New()),
		Sessions: &testSessionRegistry{rootList: want},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		workspaceRootsPath,
		nil,
	)
	request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)
	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotEmpty(t, recorder.Header().Get(headerRequestID))

	got := api.WorkspaceRootList{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &got))
	assert.Equal(t, want, got)
}

func TestServerListWorkspaceRootsRequiresAuthentication(t *testing.T) {
	t.Parallel()

	instance, err := newTestServer(Dependencies{
		APIToken: testAPIToken,
		Runtime:  newTestRuntime(uuid.New()),
		Sessions: &testSessionRegistry{rootList: api.WorkspaceRootList{
			Roots: []string{"/work/catalog"},
		}},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		workspaceRootsPath,
		nil,
	)
	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assertUnauthorizedEnvelope(t, recorder)
}
