//go:build integration

package api_test

import (
	"net/http"
	"net/url"
	"path"
	"testing"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	learnedTestWorkspacePrefix = "learned-"
	learnedTestRuleFile        = ".agents/learned/rules/go/errors.md"
	learnedTestRuleName        = "go/errors"
	learnedTestRuleBody        = "Wrap every returned error with context."
	learnedTestScopedFile      = ".agents/learned/rules/sql.md"
	learnedTestScopedBody      = "---\npaths: [\"**/*.sql\"]\n---\nUse bound parameters."
	learnedTestMessage         = "save what you learned"
	learnedTestFinalAnswer     = "saved two learned rules"
	learnedTestListPath        = apiBasePath + "/session/learned"
	learnedTestFilePath        = apiBasePath + "/session/learned/file"
)

// A user can list what the agent learned in a workspace, read it, and delete
// it, through the real controller and its filesystem. The agent writes the
// learned files with writeFile, as it does when it saves a lesson. It writes
// them into a directory it creates, because fixture files are copied in as
// root, which would leave a learned file the controller cannot delete.
func TestAPILearnedFilesListReadAndDelete(t *testing.T) {
	writer := openFreshWorkspace(t, learnedTestWorkspacePrefix, nil)
	connection := openSessionConnection(t, writer.sessionID)
	learnedWorkspace := path.Join(
		testinfra.ContainerWorkingDirectory,
		learnedTestWorkspacePrefix+uuid.NewString(),
	)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: learnedTestMessage,
		Calls: []testinfra.ScriptedToolCall{
			{Name: "writeFile", Arguments: map[string]any{
				"path":    path.Join(learnedWorkspace, learnedTestRuleFile),
				"content": learnedTestRuleBody,
			}},
			{Name: "writeFile", Arguments: map[string]any{
				"path":    path.Join(learnedWorkspace, learnedTestScopedFile),
				"content": learnedTestScopedBody,
			}},
		},
		FinalAnswer: learnedTestFinalAnswer,
	})

	sendSessionMessage(t, connection, writer.sessionID, learnedTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	headers := withHeader(authenticatedHeaders(), headerSessionID,
		openAPIWorkspaceSessionAt(t, learnedWorkspace).String())

	response := apiRequest(t, http.MethodGet, learnedTestListPath, nil,
		headers)
	requireAPIStatus(t, response, http.StatusOK)

	list := decodeResponse[api.LearnedFileList](t, response)
	workspaceFiles := map[string]api.LearnedFile{}
	for _, file := range list.Items {
		if file.Scope == api.LearnedScopeWorkspace {
			workspaceFiles[file.Name] = file
		}
	}

	require.Contains(t, workspaceFiles, learnedTestRuleName)
	require.Contains(t, workspaceFiles, "sql")
	assert.True(t, workspaceFiles[learnedTestRuleName].AlwaysLoaded)
	assert.Positive(t, workspaceFiles[learnedTestRuleName].EstimatedTokens)
	assert.False(t, workspaceFiles["sql"].AlwaysLoaded)

	query := url.Values{
		"scope": {string(api.LearnedScopeWorkspace)},
		"kind":  {string(api.LearnedKindRule)},
		"name":  {learnedTestRuleName},
	}.Encode()

	response = apiRequest(t, http.MethodGet,
		learnedTestFilePath+"?"+query, nil, headers)
	requireAPIStatus(t, response, http.StatusOK)
	assert.Equal(t, learnedTestRuleBody,
		decodeResponse[api.LearnedFileContent](t, response).Content)

	response = apiRequest(t, http.MethodDelete,
		learnedTestFilePath+"?"+query, nil, headers)
	requireAPIStatus(t, response, http.StatusNoContent)

	response = apiRequest(t, http.MethodGet,
		learnedTestFilePath+"?"+query, nil, headers)
	requireAPIStatus(t, response, http.StatusNotFound)
}

// A name that climbs out of the learned directory is refused before the
// controller touches the filesystem.
func TestAPILearnedFileRefusesAPathOutOfItsDirectory(t *testing.T) {
	workspace := openFreshWorkspace(t, learnedTestWorkspacePrefix, nil)
	headers := withHeader(authenticatedHeaders(), headerSessionID,
		workspace.sessionID.String())

	query := url.Values{
		"scope": {string(api.LearnedScopeWorkspace)},
		"kind":  {string(api.LearnedKindRule)},
		"name":  {"../../" + freshWorkspaceFixtureFile},
	}.Encode()

	response := apiRequest(t, http.MethodDelete,
		learnedTestFilePath+"?"+query, nil, headers)
	requireAPIStatus(t, response, http.StatusBadRequest)
}
