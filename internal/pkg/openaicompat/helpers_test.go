package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/require"
)

type runnerFunc func(context.Context, control.ConversationRequest) (
	*control.ConversationResult, error,
)

func (f runnerFunc) Run(
	ctx context.Context,
	request control.ConversationRequest,
) (*control.ConversationResult, error) {
	return f(ctx, request)
}

type testModels struct{}

func (testModels) ListModels() api.ModelList {
	return api.ModelList{Models: []api.Model{
		{Name: testModel, ConnectionName: "example", ImageInput: true},
		{Name: testOtherModel, ConnectionName: "example"},
	}}
}

func testDependencies(run runnerFunc) Dependencies {
	return Dependencies{
		Runner: run, Models: testModels{}, DefaultModel: testModel,
		DefaultWorkspace: testWorkspace, MaxRequestBytes: testBodyBytes,
		MaxImages: testMaxImages, MaxImageBytes: testMaxImageBytes,
	}
}

func testResult() *control.ConversationResult {
	return &control.ConversationResult{
		SessionID: uuid.New(), State: api.TurnStateCompleted,
		Text: "Answer", Model: testModel,
		Usage: control.ConversationUsage{
			PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10,
		},
	}
}

func testHandler(t *testing.T, dependencies Dependencies) http.Handler {
	t.Helper()

	handler, err := New(dependencies)
	require.NoError(t, err)

	return handler
}

func postRequest(ctx context.Context, body string) *http.Request {
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, completionsPath,
		strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	return request
}

func encodedBody(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return string(encoded)
}
