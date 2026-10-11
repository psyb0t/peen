//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path"
	"testing"

	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// policyTestHome stands in for an operator's home inside the workspace
	// root, holding the controller's state the way an install with its root
	// at $HOME and its state under ~/.config/peen does.
	policyTestHome         = "policy-home"
	policyTestStateEnv     = "PEEN_STATE_DIR"
	policyTestStateSubpath = ".config/peen/state"
	policyTestSibling      = "project"
	policyTestAuditLogs    = "logs"
)

// A workspace is bind-mounted into a Docker worker. One that is, holds, or sits
// inside the controller's state directory would hand that worker peen.db with
// every session's transcript and every session's worker socket, so the
// controller refuses it with 403 WORKSPACE_NOT_ALLOWED and creates no session.
func TestAPIRefusesAWorkspaceExposingTheStateDirectory(t *testing.T) {
	home := path.Join(testinfra.ContainerWorkingDirectory, policyTestHome)
	stateDirectory := path.Join(home, policyTestStateSubpath)

	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment: map[string]string{policyTestStateEnv: stateDirectory},
	})

	testCases := []struct {
		name      string
		workspace string
	}{
		{name: "contains the state directory", workspace: home},
		{
			name:      "contains it one level up",
			workspace: path.Dir(stateDirectory),
		},
		{name: "is the state directory", workspace: stateDirectory},
		{
			name:      "sits inside the state directory",
			workspace: path.Join(stateDirectory, policyTestAuditLogs),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			response := openVariantWorkspace(t, variant, tc.workspace)

			requireAPIStatus(t, response, http.StatusForbidden)
			assertErrorCode(t, response, server.ErrorCodeWorkspaceNotAllowed)
			assert.NotContains(t, string(response.Body), stateDirectory)
		})
	}

	assert.Empty(t, listVariantSessions(t, variant).Items)

	// A directory beside the state directory stays usable, so the refusals
	// above are the state directory's and not a policy refusing everything.
	sibling := path.Join(home, policyTestSibling)
	require.NoError(t, variant.WriteWorkspaceFile(
		t.Context(),
		path.Join(policyTestHome, policyTestSibling, freshWorkspaceFixtureFile),
		[]byte(freshWorkspaceFixtureContent),
	))

	opened := openVariantWorkspace(t, variant, sibling)
	requireAPIStatus(t, opened, http.StatusOK)

	sessions := listVariantSessions(t, variant).Items
	require.Len(t, sessions, 1)
	assert.Equal(t, sibling, sessions[0].Workspace)
}

func openVariantWorkspace(
	t *testing.T,
	variant *testinfra.Infra,
	workspace string,
) *apiResponse {
	t.Helper()

	body, err := json.Marshal(map[string]string{"workspace": workspace})
	require.NoError(t, err)

	return variantRequest(t, variant, http.MethodPost, sessionOpenPath, body)
}

func listVariantSessions(
	t *testing.T,
	variant *testinfra.Infra,
) api.SessionPage {
	t.Helper()

	response := variantRequest(
		t,
		variant,
		http.MethodGet,
		apiBasePath+"/sessions",
		nil,
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.SessionPage](t, response)
}

func variantRequest(
	t *testing.T,
	variant *testinfra.Infra,
	method string,
	requestPath string,
	body []byte,
) *apiResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		method,
		variant.APIURL(requestPath),
		bytes.NewReader(body),
	)
	require.NoError(t, err)

	for name, value := range authenticatedHeaders() {
		if body == nil && name == headerContentType {
			continue
		}

		request.Header.Set(name, value)
	}

	response, err := variant.HTTPClient().Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()

	return readAPIResponse(t, response)
}
