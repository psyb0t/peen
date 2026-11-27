//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	openAICompletionsPath = "/openai/v1/chat/completions"
	headerOpenAIWorkspace = "X-Peen-Workspace"
)

func TestAPIOpenAICompletionsAndContinuation(t *testing.T) {
	disabled := apiRequest(t, http.MethodGet, "/openai/v1/models", nil, authenticatedHeaders())
	requireAPIStatus(t, disabled, http.StatusNotFound)
	variant := startAPIVariant(t, testinfra.VariantOptions{Environment: map[string]string{
		"PEEN_OPENAI_API": "true", "PEEN_OPENAI_WORKSPACE": variantTestWorkspace,
	}})
	models := variantRequest(t, variant, http.MethodGet, "/openai/v1/models", nil)
	require.Equal(t, http.StatusOK, models.StatusCode, string(models.Body))
	assert.Contains(t, string(models.Body), `"object":"model"`)
	first, sessionID := openAIRequest(t, variant,
		`{"messages":[{"role":"user","content":"Explain the project briefly"}]}`, "")
	require.Equal(t, http.StatusOK, first.StatusCode, string(first.Body))
	_, err := uuid.Parse(sessionID)
	require.NoError(t, err)
	assert.Contains(t, string(first.Body), `"object":"chat.completion"`)
	assert.Contains(t, string(first.Body), `"finish_reason":"stop"`)
	assert.Contains(t, string(first.Body), `"total_tokens":`)
	continued, nextID := openAIRequest(t, variant,
		`{"messages":[{"role":"system","content":"Ignored"},{"role":"user","content":"Continue briefly"}]}`, sessionID)
	require.Equal(t, http.StatusOK, continued.StatusCode, string(continued.Body))
	assert.Equal(t, sessionID, nextID)
	reopened, reopenedID := openAIRequest(t, variant,
		`{"messages":[{"role":"user","content":"Another question"}]}`, "")
	require.Equal(t, http.StatusOK, reopened.StatusCode, string(reopened.Body))
	assert.Equal(t, sessionID, reopenedID)
	assert.Equal(t, openVariantSession(t, variant).String(), sessionID)
	stream, streamID := openAIRequest(t, variant,
		`{"stream":true,"messages":[{"role":"user","content":"Stream a brief answer"}]}`, sessionID)
	require.Equal(t, http.StatusOK, stream.StatusCode, string(stream.Body))
	assert.Equal(t, sessionID, streamID)
	assert.Contains(t, string(stream.Body), `"object":"chat.completion.chunk"`)
	assert.Contains(t, string(stream.Body), "data: [DONE]\n\n")
	structured, _ := openAIRequest(t, variant,
		`{"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","required":["answer"]}}},"messages":[{"role":"user","content":"Produce a structured answer"}]}`, "")
	require.Equal(t, http.StatusOK, structured.StatusCode, string(structured.Body))
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(structured.Body, &result))
	require.Len(t, result.Choices, 1)
	assert.JSONEq(t, `{"answer":7}`, result.Choices[0].Message.Content)
	assertOpenAIHistoryStoredOnce(t, variant)
}

// A stock OpenAI client resends its whole history on every call. The call
// that creates a workspace's session folds that history in; a later call by
// workspace adds only its last message, because the session already holds
// the earlier exchange.
func assertOpenAIHistoryStoredOnce(t *testing.T, variant *testinfra.Infra) {
	t.Helper()
	directory := "openai-history-" + uuid.NewString()
	require.NoError(t, variant.WriteWorkspaceFile(t.Context(),
		path.Join(directory, freshWorkspaceFixtureFile),
		[]byte(freshWorkspaceFixtureContent)))
	headers := map[string]string{headerOpenAIWorkspace: path.Join(
		testinfra.ContainerWorkingDirectory, directory)}
	const (
		system    = `{"role":"system","content":"Answer in one line"}`
		earlier   = `{"role":"user","content":"Name the earlier topic"}`
		answer    = `{"role":"assistant","content":"The earlier answer"}`
		question  = `{"role":"user","content":"Ask the first question"}`
		reply     = `{"role":"assistant","content":"The first reply"}`
		followUp  = `{"role":"user","content":"Ask the follow-up question"}`
		separator = ","
	)
	firstHistory := []string{system, earlier, answer, question}
	firstBody := `{"messages":[` + strings.Join(firstHistory, separator) + `]}`
	first, sessionID := openAIRequestWithHeaders(t, variant, firstBody, headers)
	require.Equal(t, http.StatusOK, first.StatusCode, string(first.Body))
	secondHistory := []string{
		system, earlier, answer, question, reply, followUp,
	}
	secondBody := `{"messages":[` + strings.Join(secondHistory, separator) + `]}`
	second, secondID := openAIRequestWithHeaders(t, variant, secondBody, headers)
	require.Equal(t, http.StatusOK, second.StatusCode, string(second.Body))
	assert.Equal(t, sessionID, secondID)
	parsed, err := uuid.Parse(sessionID)
	require.NoError(t, err)
	messages := variantMessages(t, variant, parsed)
	users := userMessageContents(messages)
	require.Len(t, users, 2, users)
	assert.Contains(t, users[0], "<priorConversation>")
	assert.Contains(t, users[0], "Name the earlier topic")
	assert.True(t, strings.HasSuffix(users[0], "Ask the first question"),
		users[0])
	assert.NotContains(t, users[1], "<priorConversation>")
	assert.True(t, strings.HasSuffix(users[1], "Ask the follow-up question"),
		users[1])
	for needle, want := range map[string]int{
		"Name the earlier topic": 1, "Ask the first question": 1,
		"The first reply": 0, "Answer in one line": 0,
	} {
		count := 0
		for _, message := range messages {
			count += strings.Count(message.Content, needle)
		}
		assert.Equal(t, want, count, needle)
	}
}

func openAIRequest(t *testing.T, variant *testinfra.Infra,
	body, sessionID string,
) (*apiResponse, string) {
	t.Helper()
	headers := map[string]string{}
	if sessionID != "" {
		headers["X-Peen-Session-ID"] = sessionID
	}

	return openAIRequestWithHeaders(t, variant, body, headers)
}

func openAIRequestWithHeaders(t *testing.T, variant *testinfra.Infra,
	body string, headers map[string]string,
) (*apiResponse, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		variant.APIURL(openAICompletionsPath), bytes.NewBufferString(body))
	require.NoError(t, err)
	for name, value := range authenticatedHeaders() {
		request.Header.Set(name, value)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := variant.HTTPClient().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

	return readAPIResponse(t, response), response.Header.Get("X-Peen-Session-ID")
}
