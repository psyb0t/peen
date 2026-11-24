//go:build integration

package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"testing"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fileAPIPath = apiBasePath + "/session/files"

func fileAPIWorkspace(t *testing.T) freshWorkspace {
	t.Helper()
	directory := path.Join(testinfra.ContainerWorkingDirectory,
		"file-api-"+uuid.NewString())
	fileAPIFixtureCommand(t, "mkdir", "-p", directory)

	return freshWorkspace{
		directory: directory,
		sessionID: openAPIWorkspaceSessionAt(t, directory),
	}
}

func fileAPIFixtureCommand(t *testing.T, arguments ...string) {
	t.Helper()
	code, output, err := integrationInfra.App.Exec(t.Context(), arguments)
	require.NoError(t, err)
	text, err := io.ReadAll(output)
	require.NoError(t, err)
	require.Equal(t, 0, code, "%s", text)
}

func fileAPIWrite(
	t *testing.T,
	workspace freshWorkspace,
	name string,
	content []byte,
) *apiResponse {
	t.Helper()
	create := true
	body, err := json.Marshal(api.WorkspaceFileWrite{
		Path: name, ContentBase64: base64.StdEncoding.EncodeToString(content),
		CreateDirectories: &create,
	})
	require.NoError(t, err)

	return apiRequest(t, http.MethodPut, fileAPIPath+"/content", body,
		withHeader(authenticatedHeaders(), headerSessionID,
			workspace.sessionID.String()))
}

func TestAPIWorkspaceFilesRoundTripAndLeafDeletion(t *testing.T) {
	workspace := fileAPIWorkspace(t)
	headers := withHeader(authenticatedHeaders(), headerSessionID,
		workspace.sessionID.String())
	const name = "nested/binary.dat"
	content := bytes.Repeat([]byte{0, 1, 255}, 400000)
	response := fileAPIWrite(t, workspace, name, content)
	requireAPIStatus(t, response, http.StatusOK)
	assert.EqualValues(t, len(content),
		decodeResponse[api.WorkspaceFileEntry](t, response).Size)
	response = apiRequest(t, http.MethodGet, fileAPIPath+"?path=nested",
		nil, headers)
	requireAPIStatus(t, response, http.StatusOK)
	listing := decodeResponse[api.WorkspaceFileListing](t, response)
	require.Len(t, listing.Entries, 1)
	assert.Equal(t, path.Base(name), listing.Entries[0].Name)
	response = apiRequest(t, http.MethodGet,
		fileAPIPath+"/content?path="+url.QueryEscape(name), nil, headers)
	requireAPIStatus(t, response, http.StatusOK)
	decoded := decodeResponse[api.WorkspaceFileContent](t, response)
	assert.Equal(t, base64.StdEncoding.EncodeToString(content),
		decoded.ContentBase64)
	response = apiRequest(t, http.MethodDelete,
		fileAPIPath+"?path=nested", nil, headers)
	requireAPIStatus(t, response, http.StatusConflict)
	response = apiRequest(t, http.MethodDelete,
		fileAPIPath+"?path="+url.QueryEscape(name), nil, headers)
	requireAPIStatus(t, response, http.StatusNoContent)
	response = apiRequest(t, http.MethodDelete,
		fileAPIPath+"?path=nested", nil, headers)
	requireAPIStatus(t, response, http.StatusNoContent)
	response = apiRequest(t, http.MethodGet,
		fileAPIPath+"/content?path="+url.QueryEscape(name), nil, headers)
	requireAPIStatus(t, response, http.StatusNotFound)
}

func TestAPIWorkspaceFilesRefuseEscapeAndOversize(t *testing.T) {
	workspace := fileAPIWorkspace(t)
	headers := withHeader(authenticatedHeaders(), headerSessionID,
		workspace.sessionID.String())
	fileAPIFixtureCommand(t, "ln", "-s", "/etc", path.Join(
		workspace.directory, "outside"))
	for _, name := range []string{"../escape", "/etc/passwd", "outside/passwd"} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			response := apiRequest(t, method,
				fileAPIPath+"?path="+url.QueryEscape(name), nil, headers)
			requireAPIStatus(t, response, http.StatusForbidden)
			assertErrorCode(t, response, server.ErrorCodeWorkspaceNotAllowed)
			assert.NotContains(t, string(response.Body), workspace.directory)
		}
		response := fileAPIWrite(t, workspace, name, []byte("refused"))
		requireAPIStatus(t, response, http.StatusForbidden)
	}
	oversize := bytes.Repeat([]byte("x"), (16<<20)+1)
	response := fileAPIWrite(t, workspace, "too-large", oversize)
	requireAPIStatus(t, response, http.StatusBadRequest)
	require.NoError(t, integrationInfra.WriteWorkspaceFile(t.Context(),
		path.Join(path.Base(workspace.directory), "large-read"), oversize))
	response = apiRequest(t, http.MethodGet,
		fileAPIPath+"/content?path=large-read", nil, headers)
	requireAPIStatus(t, response, http.StatusBadRequest)
}
