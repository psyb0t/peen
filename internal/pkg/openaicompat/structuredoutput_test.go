package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaByteLimitIsCheckedBeforeCompilation(t *testing.T) {
	t.Parallel()

	dependencies := testDependencies(runnerFunc(nil))
	dependencies.MaxOutputSchemaBytes = 32
	body := encodedBody(t, map[string]any{
		"messages": []map[string]string{{"role": roleUser, "content": "Question"}},
		"response_format": map[string]any{
			"type": formatSchema, "json_schema": map[string]any{
				"schema": map[string]string{"description": strings.Repeat("x", 64)},
			},
		},
	})
	response := httptest.NewRecorder()
	testHandler(t, dependencies).ServeHTTP(response, postRequest(t.Context(), body))
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestStructuredCompletionMapsAndReturnsValidatedOutput(t *testing.T) {
	t.Parallel()

	for _, format := range []string{
		`{"type":"json_object"}`,
		`{"type":"json_schema","json_schema":{"name":"answer",` +
			`"strict":true,"schema":{"type":"object"}}}`,
	} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			result := testResult()
			result.Output = json.RawMessage(`{"answer":true}`)
			h := testHandler(t, testDependencies(func(
				_ context.Context,
				request control.ConversationRequest,
			) (*control.ConversationResult, error) {
				assert.JSONEq(t, objectSchema,
					string(request.Message.OutputSchema))

				return result, nil
			}))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, postRequest(t.Context(), `{"response_format":`+format+
				`,"messages":[{"role":"user","content":"Question"}]}`))

			require.Equal(t, http.StatusOK, response.Code)
			var answer completion
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &answer))
			require.Len(t, answer.Choices, 1)
			assert.Equal(t, string(result.Output),
				answer.Choices[0].Message.Content)
		})
	}
}

func TestStructuredStreamNeverWritesUnvalidatedDeltas(t *testing.T) {
	t.Parallel()

	result := testResult()
	result.Output = json.RawMessage(`{"answer":true}`)
	response := httptest.NewRecorder()
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		require.NoError(t, emitText(request, result.SessionID, "INVALID_DRAFT"))
		assert.Empty(t, response.Body.String())
		assert.False(t, response.Flushed)

		return result, nil
	}))
	h.ServeHTTP(response, postRequest(t.Context(), `{"stream":true,
		"response_format":{"type":"json_object"},
		"messages":[{"role":"user","content":"Question"}]}`))

	chunks := streamChunks(t, response.Body.String())
	require.Len(t, chunks, 3)
	assert.Equal(t, string(result.Output), *chunks[1].Choices[0].Delta.Content)
	assert.NotContains(t, response.Body.String(), "INVALID_DRAFT")
}

func TestStructuredFailureHidesDraftAndInternalError(t *testing.T) {
	t.Parallel()

	result := testResult()
	result.OutputError = "/private/schema EXAMPLE-TOKEN"
	result.Text = "INVALID_DRAFT"
	h := testHandler(t, testDependencies(func(
		context.Context,
		control.ConversationRequest,
	) (*control.ConversationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), `{"stream":true,
		"response_format":{"type":"json_object"},
		"messages":[{"role":"user","content":"Question"}]}`))

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Contains(t, response.Body.String(), codeOutputInvalid)
	assert.NotContains(t, response.Body.String(), "INVALID_DRAFT")
	assert.NotContains(t, response.Body.String(), "EXAMPLE-TOKEN")
	assert.NotContains(t, response.Body.String(), "/private/schema")
}

func TestSchemaRefusesExternalReferencesBeforeRunning(t *testing.T) {
	t.Parallel()

	for _, reference := range []string{
		"https://invalid.example/schema", "file:///private/schema.json",
	} {
		body := encodedBody(t, map[string]any{
			"messages": []map[string]string{
				{"role": roleUser, "content": "Hello"},
			},
			"response_format": map[string]any{
				"type": formatSchema, "json_schema": map[string]any{
					"schema": map[string]string{"$ref": reference},
				},
			},
		})
		assertRejected(t, body)
	}
}
