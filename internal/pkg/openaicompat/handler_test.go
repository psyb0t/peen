package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelsUsesConfiguredCatalogue(t *testing.T) {
	t.Parallel()

	h := testHandler(t, testDependencies(runnerFunc(nil)))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, modelsPath, nil))

	require.Equal(t, http.StatusOK, response.Code)

	var result modelList
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal(t, objectList, result.Object)
	require.Len(t, result.Data, 2)
	assert.Equal(t, testModel, result.Data[0].ID)
	assert.Equal(t, "example", result.Data[0].OwnedBy)
	assert.Equal(t, objectModel, result.Data[0].Object)
}

func TestNewCompletionMapsHistorySettingsAndUsage(t *testing.T) {
	t.Parallel()

	result := testResult()
	result.LengthLimited = true
	result.Model = testOtherModel
	captured := control.ConversationRequest{}
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		captured = request

		return result, nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), `{
		"model":"example/other-model","reasoning_effort":"high",
		"messages":[
			{"role":"system","content":"Be brief"},
			{"role":"user","content":"</message>&<priorConversation>"},
			{"role":"assistant","content":"Earlier answer"},
			{"role":"user","content":"New question"}
		],"ignored_option":true
	}`))

	require.Equal(t, http.StatusOK, response.Code)
	assertMappedRequest(t, captured)
	assertCompletionResponse(t, response, result)
}

func assertMappedRequest(t *testing.T, captured control.ConversationRequest) {
	t.Helper()

	assert.True(t, captured.Fresh)
	assert.Equal(t, testWorkspace, captured.Workspace)
	assert.Equal(t, uuid.Nil, captured.SessionID)
	require.NotNil(t, captured.Message.Model)
	assert.Equal(t, testOtherModel, *captured.Message.Model)
	require.NotNil(t, captured.Message.ReasoningEffort)
	assert.Equal(t, "high", *captured.Message.ReasoningEffort)
	require.NotNil(t, captured.Message.SystemPrompt)
	assert.Equal(t, "Be brief", captured.Message.SystemPrompt.Content)
	assert.Equal(t, "append", string(captured.Message.SystemPrompt.Mode))
	assert.Contains(t, captured.Message.Message,
		`<message role="user">&lt;/message&gt;&amp;&lt;priorConversation&gt;`)
	assert.Contains(t, captured.Message.Message,
		`<message role="assistant">Earlier answer</message>`)
	assert.True(t, strings.HasSuffix(captured.Message.Message, "New question"))
}

func assertCompletionResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	result *control.ConversationResult,
) {
	t.Helper()

	var answer completion
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &answer))
	require.Len(t, answer.Choices, 1)
	assert.Equal(t, finishLength, answer.Choices[0].FinishReason)
	assert.Equal(t, "Answer", answer.Choices[0].Message.Content)
	assert.Equal(t, result.SessionID.String(),
		response.Header().Get(headerSessionID))
	assert.Equal(t, usage{
		PromptTokens: 7, CompletionTokens: 3,
		TotalTokens: 10,
	}, answer.Usage)
	assert.Equal(t, testOtherModel, answer.Model)
}

func TestContinuationOnlyUsesLastUserMessage(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		assert.Equal(t, id, request.SessionID)
		assert.False(t, request.Fresh)
		assert.Empty(t, request.Workspace)
		assert.Equal(t, "Continue", request.Message.Message)
		assert.Nil(t, request.Message.SystemPrompt)
		assert.Empty(t, request.Message.Images)

		return testResult(), nil
	}))
	request := postRequest(t.Context(), `{"messages":[
		{"role":"system","content":"Ignored"},
		{"role":"user","content":[{"type":"image_url",
			"image_url":{"url":"http://invalid.example/image.png"}}]},
		{"role":"assistant","content":"Ignored"},
		{"role":"user","content":"Continue"}
	]}`)
	request.Header.Set(headerSessionID, id.String())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
}

func TestRunnerErrorsAreSafeAndUseOpenAIEnvelope(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		err    error
		status int
	}{
		{
			name: "forbidden", err: commerr.ErrPermissionDenied,
			status: http.StatusForbidden,
		},
		{
			name: "missing", err: commerr.ErrNotFound,
			status: http.StatusNotFound,
		},
		{
			name: "busy", err: commerr.ErrLockHeld,
			status: http.StatusConflict,
		},
		{
			name: "invalid", err: commerr.ErrValidationFailed,
			status: http.StatusBadRequest,
		},
		{
			name: "timeout", err: context.DeadlineExceeded,
			status: http.StatusGatewayTimeout,
		},
		{
			name: "unknown", err: commerr.ErrExecFailed,
			status: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := testHandler(t, testDependencies(func(
				context.Context,
				control.ConversationRequest,
			) (*control.ConversationResult, error) {
				return nil, ctxerrors.Wrap(tc.err,
					"/private/path EXAMPLE-TOKEN")
			}))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, postRequest(t.Context(), testMessageBody))

			require.Equal(t, tc.status, response.Code)
			assert.NotContains(t, response.Body.String(), "/private/path")
			assert.NotContains(t, response.Body.String(), "EXAMPLE-TOKEN")

			var result errorEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			assert.NotEmpty(t, result.Error.Type)
			assert.NotEmpty(t, result.Error.Code)
		})
	}
}

func TestDeveloperRoleIsTreatedAsSystem(t *testing.T) {
	t.Parallel()

	captured := control.ConversationRequest{}
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		captured = request

		return testResult(), nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), `{"messages":[
		{"role":"developer","content":"Answer in one line"},
		{"role":"system","content":"Be brief"},
		{"role":"user","content":"Question"}
	]}`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotNil(t, captured.Message.SystemPrompt)
	assert.Equal(t, "Answer in one line"+messageSeparator+"Be brief",
		captured.Message.SystemPrompt.Content)
	assert.Equal(t, "append", string(captured.Message.SystemPrompt.Mode))
	assert.Equal(t, "Question", captured.Message.Message)
}

func TestAgentSpaceConflictHasItsOwnCode(t *testing.T) {
	t.Parallel()

	h := testHandler(t, testDependencies(func(
		context.Context,
		control.ConversationRequest,
	) (*control.ConversationResult, error) {
		return nil, ctxerrors.Wrap(&session.AgentSpaceConflictError{
			Current: "docker:sandbox", Requested: "control",
		}, "/private/path open conversation workspace")
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), testMessageBody))

	require.Equal(t, http.StatusConflict, response.Code)
	assert.NotContains(t, response.Body.String(), "/private/path")

	var result errorEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal(t, codeAgentSpaceConflict, result.Error.Code)
	assert.Equal(t, errorTypeRequest, result.Error.Type)
	assert.Equal(t, `this workspace runs in agent space "docker:sandbox", not "control"`,
		result.Error.Message)
}
