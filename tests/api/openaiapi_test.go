//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const openAICompletionsPath = "/openai/v1/chat/completions"

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
	fresh, freshID := openAIRequest(t, variant,
		`{"messages":[{"role":"user","content":"Another question"}]}`, "")
	require.Equal(t, http.StatusOK, fresh.StatusCode, string(fresh.Body))
	assert.NotEqual(t, sessionID, freshID)
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
}

func openAIRequest(t *testing.T, variant *testinfra.Infra,
	body, sessionID string,
) (*apiResponse, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		variant.APIURL(openAICompletionsPath), bytes.NewBufferString(body))
	require.NoError(t, err)
	for name, value := range authenticatedHeaders() {
		request.Header.Set(name, value)
	}
	if sessionID != "" {
		request.Header.Set("X-Peen-Session-ID", sessionID)
	}
	response, err := variant.HTTPClient().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

	return readAPIResponse(t, response), response.Header.Get("X-Peen-Session-ID")
}
