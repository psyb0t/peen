//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"path"
	"testing"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	agentSpacesPath         = apiBasePath + "/agent-spaces"
	sessionOpenPath         = apiBasePath + "/sessions/open"
	sessionReconfigurePath  = sessionPath + "/reconfigure"
	agentSpaceDecisionsPath = sessionPath + "/agent-space-decisions"
	removedSpacesPath       = apiBasePath + "/execution-profiles"
	removedDecisionsPath    = sessionPath + "/profile-decisions"

	agentSpaceControl       = "control"
	agentSpaceDockerDefault = "docker:default"
	agentSpaceUndefined     = "docker:never-defined"
	agentSpaceWorkspacePre  = "agent-space-workspace-"
	agentSpaceReason        = "move the workspace to its sealed container"
)

func openWorkspaceInAgentSpace(
	t *testing.T,
	agentSpace string,
) *apiResponse {
	t.Helper()

	directory := agentSpaceWorkspacePre + uuid.NewString()
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		path.Join(directory, freshWorkspaceFixtureFile),
		[]byte(freshWorkspaceFixtureContent),
	))

	return openWorkspaceDirectory(t, directory, agentSpace)
}

func openWorkspaceDirectory(
	t *testing.T,
	directory string,
	agentSpace string,
) *apiResponse {
	t.Helper()

	request := map[string]string{
		"workspace": path.Join(testinfra.ContainerWorkingDirectory, directory),
	}
	if agentSpace != "" {
		request["agentSpace"] = agentSpace
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	return apiRequest(
		t,
		http.MethodPost,
		sessionOpenPath,
		body,
		authenticatedHeaders(),
	)
}

// The controller in this suite reaches no Docker socket, so it offers the
// control agent space and defaults to it, and still lists the built-in Docker
// profile with its grants.
func TestAPIListsTheAgentSpacesWithTheirGrants(t *testing.T) {
	response := apiRequest(
		t,
		http.MethodGet,
		agentSpacesPath,
		nil,
		authenticatedHeaders(),
	)
	requireAPIStatus(t, response, http.StatusOK)

	list := decodeResponse[api.AgentSpaceList](t, response)
	assert.Equal(t, agentSpaceControl, list.Default)

	byName := map[string]api.AgentSpace{}
	for _, item := range list.Items {
		byName[item.Name] = item
	}

	controlSpace, found := byName[agentSpaceControl]
	require.True(t, found, "the control agent space must be offered")
	assert.Equal(t, api.AgentSpaceKindControl, controlSpace.Kind)
	assert.Nil(t, controlSpace.Docker)
	assert.False(t, controlSpace.HostRootEquivalent)

	builtIn, found := byName[agentSpaceDockerDefault]
	require.True(t, found, "the built-in default Docker profile must be offered")
	assert.Equal(t, api.AgentSpaceKindDocker, builtIn.Kind)
	require.NotNil(t, builtIn.Profile)
	assert.Equal(t, "default", *builtIn.Profile)
	require.NotNil(t, builtIn.Docker)
	assert.Equal(t, api.AgentSpaceDockerNone, *builtIn.Docker)
	require.NotNil(t, builtIn.Network)
	assert.Equal(t, api.AgentSpaceNetworkBridge, *builtIn.Network)
	require.NotNil(t, builtIn.Sudo)
	assert.False(t, *builtIn.Sudo)
	assert.False(t, builtIn.HostRootEquivalent)
	assert.Empty(t, builtIn.CapabilityWarnings)
}

func TestAPIDoesNotServeTheRemovedProfileRoutes(t *testing.T) {
	headers := withHeader(
		authenticatedHeaders(),
		headerSessionID,
		uuid.NewString(),
	)

	for _, route := range []string{removedSpacesPath, removedDecisionsPath} {
		t.Run(route, func(t *testing.T) {
			response := apiRequest(t, http.MethodGet, route, nil, headers)

			assert.Equal(t, http.StatusNotFound, response.StatusCode)
		})
	}
}

func TestAPIOpensAWorkspaceInANamedAgentSpace(t *testing.T) {
	response := openWorkspaceInAgentSpace(t, agentSpaceDockerDefault)
	requireAPIStatus(t, response, http.StatusOK)

	opened := decodeResponse[api.OpenedSession](t, response)
	require.NotNil(t, opened.Session.AgentSpace)
	assert.Equal(t, agentSpaceDockerDefault, *opened.Session.AgentSpace)
}

func TestAPIOpensAWorkspaceInTheControllerDefaultAgentSpace(t *testing.T) {
	response := openWorkspaceInAgentSpace(t, "")
	requireAPIStatus(t, response, http.StatusOK)

	opened := decodeResponse[api.OpenedSession](t, response)
	require.NotNil(t, opened.Session.AgentSpace)
	assert.Equal(t, agentSpaceControl, *opened.Session.AgentSpace)
}

func TestAPIRefusesAnAgentSpaceTheDeploymentDoesNotOffer(t *testing.T) {
	response := openWorkspaceInAgentSpace(t, agentSpaceUndefined)

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assertErrorCode(t, response, server.ErrorCodeAgentSpaceNotAllowed)
}

func TestAPIReconfiguresAnAgentSpaceAndRecordsTheDecision(t *testing.T) {
	response := openWorkspaceInAgentSpace(t, agentSpaceControl)
	requireAPIStatus(t, response, http.StatusOK)

	opened := decodeResponse[api.OpenedSession](t, response)
	headers := withHeader(
		authenticatedHeaders(),
		headerSessionID,
		opened.Session.Id.String(),
	)

	body, err := json.Marshal(api.ReconfigureSessionRequest{
		AgentSpace: agentSpaceDockerDefault,
		Reason:     agentSpaceReason,
	})
	require.NoError(t, err)

	moved := apiRequest(
		t,
		http.MethodPost,
		sessionReconfigurePath,
		body,
		headers,
	)
	requireAPIStatus(t, moved, http.StatusOK)

	decision := decodeResponse[api.SessionAgentSpaceDecision](t, moved)
	assert.Equal(t, agentSpaceDockerDefault, decision.ToAgentSpace)
	require.NotNil(t, decision.FromAgentSpace)
	assert.Equal(t, agentSpaceControl, *decision.FromAgentSpace)
	assert.Equal(t, agentSpaceReason, decision.Reason)

	listed := apiRequest(
		t,
		http.MethodGet,
		agentSpaceDecisionsPath,
		nil,
		headers,
	)
	requireAPIStatus(t, listed, http.StatusOK)

	page := decodeResponse[api.SessionAgentSpaceDecisionPage](t, listed)
	require.Len(t, page.Items, 1)
	assert.Equal(t, agentSpaceDockerDefault, page.Items[0].ToAgentSpace)

	session := getSession(t, opened.Session.Id)
	require.NotNil(t, session.AgentSpace)
	assert.Equal(t, agentSpaceDockerDefault, *session.AgentSpace)

	refusedBody, err := json.Marshal(api.ReconfigureSessionRequest{
		AgentSpace: agentSpaceUndefined,
		Reason:     agentSpaceReason,
	})
	require.NoError(t, err)

	refused := apiRequest(
		t,
		http.MethodPost,
		sessionReconfigurePath,
		refusedBody,
		headers,
	)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode)
	assertErrorCode(t, refused, server.ErrorCodeAgentSpaceNotAllowed)
}

func TestAPIRejectsTheRemovedProfileFieldOnReconfigure(t *testing.T) {
	response := openWorkspaceInAgentSpace(t, "")
	requireAPIStatus(t, response, http.StatusOK)

	opened := decodeResponse[api.OpenedSession](t, response)

	moved := apiRequest(
		t,
		http.MethodPost,
		sessionReconfigurePath,
		[]byte(`{"profile":"control","reason":"old field"}`),
		withHeader(
			authenticatedHeaders(),
			headerSessionID,
			opened.Session.Id.String(),
		),
	)

	assert.Equal(t, http.StatusBadRequest, moved.StatusCode)
}

// Every session of one workspace shares its agent space. Reopening it with no
// agent space resumes it, naming another one is refused with
// AGENT_SPACE_CONFLICT, and a reconfigure moves the workspace so the new name
// is the one that opens.
func TestAPIKeepsOneAgentSpacePerWorkspace(t *testing.T) {
	directory := agentSpaceWorkspacePre + uuid.NewString()
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		path.Join(directory, freshWorkspaceFixtureFile),
		[]byte(freshWorkspaceFixtureContent),
	))

	first := openWorkspaceDirectory(t, directory, agentSpaceControl)
	requireAPIStatus(t, first, http.StatusOK)
	opened := decodeResponse[api.OpenedSession](t, first)

	refused := openWorkspaceDirectory(t, directory, agentSpaceDockerDefault)
	requireAPIStatus(t, refused, http.StatusConflict)
	assertErrorCode(t, refused, server.ErrorCodeAgentSpaceConflict)
	envelope := decodeResponse[api.Error](t, refused)
	assert.Equal(t,
		`this workspace runs in agent space "control", not "docker:default"`,
		envelope.Message)

	resumed := openWorkspaceDirectory(t, directory, "")
	requireAPIStatus(t, resumed, http.StatusOK)
	assert.Equal(t, opened.Session.Id,
		decodeResponse[api.OpenedSession](t, resumed).Session.Id)

	body, err := json.Marshal(api.ReconfigureSessionRequest{
		AgentSpace: agentSpaceDockerDefault,
		Reason:     agentSpaceReason,
	})
	require.NoError(t, err)

	moved := apiRequest(
		t,
		http.MethodPost,
		sessionReconfigurePath,
		body,
		withHeader(
			authenticatedHeaders(),
			headerSessionID,
			opened.Session.Id.String(),
		),
	)
	requireAPIStatus(t, moved, http.StatusOK)

	reopened := openWorkspaceDirectory(t, directory, agentSpaceDockerDefault)
	requireAPIStatus(t, reopened, http.StatusOK)
	assert.Equal(t, opened.Session.Id,
		decodeResponse[api.OpenedSession](t, reopened).Session.Id)

	stale := openWorkspaceDirectory(t, directory, agentSpaceControl)
	requireAPIStatus(t, stale, http.StatusConflict)
	assertErrorCode(t, stale, server.ErrorCodeAgentSpaceConflict)
}
