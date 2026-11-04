package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	learnedTestPath     = "/session/learned"
	learnedTestFilePath = "/session/learned/file"
	learnedTestRuleName = "go/errors"
)

func serveLearnedRequest(
	t *testing.T,
	registry *testSessionRegistry,
	method, path string,
	query url.Values,
) *httptest.ResponseRecorder {
	t.Helper()

	sessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		Sessions: registry,
	})
	require.NoError(t, err)

	target := apiBaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	request := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(recorder, request)

	return recorder
}

func learnedRuleQuery() url.Values {
	return url.Values{
		"scope": {string(api.LearnedScopeWorkspace)},
		"kind":  {string(api.LearnedKindRule)},
		"name":  {learnedTestRuleName},
	}
}

func TestServerListSessionLearnedFiles(t *testing.T) {
	t.Parallel()

	registry := &testSessionRegistry{
		learnedList: api.LearnedFileList{Items: []api.LearnedFile{{
			Scope:           api.LearnedScopeWorkspace,
			Kind:            api.LearnedKindRule,
			Name:            learnedTestRuleName,
			AlwaysLoaded:    true,
			EstimatedTokens: 42,
		}}},
	}

	recorder := serveLearnedRequest(t, registry, http.MethodGet,
		learnedTestPath, nil)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	list := api.LearnedFileList{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, learnedTestRuleName, list.Items[0].Name)
	assert.True(t, list.Items[0].AlwaysLoaded)
	assert.Equal(t, int64(42), list.Items[0].EstimatedTokens)
}

func TestServerListSessionLearnedFilesForAMissingSession(t *testing.T) {
	t.Parallel()

	registry := &testSessionRegistry{
		learnedErr: ctxerrors.Wrap(commerr.ErrNotFound, "load session"),
	}

	recorder := serveLearnedRequest(t, registry, http.MethodGet,
		learnedTestPath, nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

// The query names the file, and the handler hands it to the registry as is.
func TestServerGetSessionLearnedFile(t *testing.T) {
	t.Parallel()

	registry := &testSessionRegistry{
		learnedContent: api.LearnedFileContent{Content: "Wrap every error."},
	}

	recorder := serveLearnedRequest(t, registry, http.MethodGet,
		learnedTestFilePath, learnedRuleQuery())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	content := api.LearnedFileContent{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &content))
	assert.Equal(t, "Wrap every error.", content.Content)
	assert.Equal(t, []harness.LearnedFileReference{{
		Scope: harness.LearnedScopeWorkspace,
		Kind:  harness.LearnedKindRule,
		Name:  learnedTestRuleName,
	}}, registry.learnedReferences)
}

func TestServerDeleteSessionLearnedFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "deleted", wantStatus: http.StatusNoContent},
		{
			name:       "missing file",
			err:        ctxerrors.Wrap(commerr.ErrNotFound, "learned file"),
			wantStatus: http.StatusNotFound,
		},
		{
			name: "name that leaves the directory",
			err: ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"invalid learned name",
			),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := &testSessionRegistry{learnedErr: tc.err}

			recorder := serveLearnedRequest(t, registry, http.MethodDelete,
				learnedTestFilePath, learnedRuleQuery())
			assert.Equal(t, tc.wantStatus, recorder.Code,
				recorder.Body.String())
			assert.Len(t, registry.learnedReferences, 1)
		})
	}
}

// The spec allows only the two scopes, so another value never reaches the
// registry.
func TestServerGetSessionLearnedFileRejectsAnUnknownScope(t *testing.T) {
	t.Parallel()

	registry := &testSessionRegistry{}
	query := learnedRuleQuery()
	query.Set("scope", "elsewhere")

	recorder := serveLearnedRequest(t, registry, http.MethodGet,
		learnedTestFilePath, query)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Empty(t, registry.learnedReferences)
}
