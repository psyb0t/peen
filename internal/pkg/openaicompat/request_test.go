package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletionRejectsUnsupportedOptions(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		`"tools":[]`, `"tool_choice":"auto"`, `"functions":[]`,
		`"logprobs":true`, `"n":2`, `"n":0`, `"model":"missing/model"`,
		`"reasoning_effort":"unknown"`, `"response_format":{"type":"xml"}`,
		`"response_format":{"type":"json_schema"}`,
		`"response_format":{"type":"json_schema","json_schema":{"schema":[]}}`,
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()

			body := `{"messages":[{"role":"user","content":"Hello"}],` +
				field + `}`
			assertRejected(t, body)
		})
	}
}

func TestCompletionRejectsMalformedBodies(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{`, `null`, `[]`, testMessageBody + ` {}`, `{}`, `{"messages":[]}`,
		`{"messages":[{"role":"assistant","content":"No user"}]}`,
		`{"messages":[{"role":"tool","content":"Untrusted"},` +
			`{"role":"user","content":"Question"}]}`,
		`{"messages":[{"role":"user","content":{}}]}`,
		`{"messages":[{"role":"user","content":[]}]}`,
		`{"messages":[{"role":"user","content":"  "}]}`,
		`{"messages":[{"role":"user","content":[{"type":"input_audio"}]}]}`,
		`{"messages":[{"role":"user","content":"Hello"}],"stream":"yes"}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			assertRejected(t, body)
		})
	}
}

func assertRejected(t *testing.T, body string) {
	t.Helper()

	called := false
	h := testHandler(t, testDependencies(func(
		context.Context,
		control.ConversationRequest,
	) (*control.ConversationResult, error) {
		called = true

		return testResult(), nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), body))

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.False(t, called)
	assert.Contains(t, response.Body.String(), `"error":`)
}

func TestCompletionHeaderSelectors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		header    string
		value     string
		workspace string
		status    int
	}{
		{
			name: "workspace override", header: headerWorkspace,
			value: "/srv/work/other", workspace: "/srv/work/other",
			status: http.StatusOK,
		},
		{name: "missing workspace", status: http.StatusBadRequest},
		{
			name: "malformed session", header: headerSessionID,
			value: "not-an-id", status: http.StatusBadRequest,
		},
		{
			name: "nil session", header: headerSessionID,
			value: uuid.Nil.String(), status: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dependencies := testDependencies(func(
				_ context.Context,
				request control.ConversationRequest,
			) (*control.ConversationResult, error) {
				assert.Equal(t, tc.workspace, request.Workspace)

				return testResult(), nil
			})
			dependencies.DefaultWorkspace = ""
			request := postRequest(t.Context(), testMessageBody)
			request.Header.Set(tc.header, tc.value)
			response := httptest.NewRecorder()
			testHandler(t, dependencies).ServeHTTP(response, request)

			assert.Equal(t, tc.status, response.Code)
		})
	}
}

func TestCompletionBodyAndContentTypeBounds(t *testing.T) {
	t.Parallel()

	h := testHandler(t, testDependencies(runnerFunc(nil)))
	request := postRequest(t.Context(), strings.Repeat(" ", testBodyBytes)+testMessageBody)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)

	request = postRequest(t.Context(), testMessageBody)
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnsupportedMediaType, response.Code)
}

func TestNewRejectsUnusableDependencies(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*Dependencies){
		func(d *Dependencies) { d.Runner = nil },
		func(d *Dependencies) { d.Models = nil },
		func(d *Dependencies) { d.MaxRequestBytes = 0 },
		func(d *Dependencies) { d.MaxImages = -1 },
		func(d *Dependencies) { d.MaxImageBytes = 0 },
	} {
		dependencies := testDependencies(runnerFunc(nil))
		mutate(&dependencies)
		_, err := New(dependencies)
		require.Error(t, err)
	}
}

func TestRoutingAndAuthenticationErrorsKeepCompatibilityEnvelope(t *testing.T) {
	t.Parallel()

	h := testHandler(t, testDependencies(runnerFunc(nil)))
	for _, path := range []string{modelsPath, completionsPath, "/missing"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, nil))
		assert.Contains(t, response.Body.String(), `"error":`)
		assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest)
	}

	response := httptest.NewRecorder()
	require.NoError(t, WriteUnauthorized(response))
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"type":"authentication_error"`)
}

func TestInternalSetupErrorKeepsSafeEnvelope(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	require.NoError(t, WriteInternalError(response))
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Contains(t, response.Body.String(), codeTurnFailed)
}

func TestCompletionRefusesAmbiguousSessionHeaders(t *testing.T) {
	t.Parallel()

	for _, header := range []string{headerSessionID, headerWorkspace} {
		request := postRequest(t.Context(), testMessageBody)
		request.Header.Add(header, "first")
		request.Header.Add(header, "second")
		response := httptest.NewRecorder()
		h := testHandler(t, testDependencies(runnerFunc(nil)))
		h.ServeHTTP(response, request)

		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
}
