package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerListModels(t *testing.T) {
	t.Parallel()

	runtime := newTestRuntime(uuid.New())
	runtime.modelList = api.ModelList{Models: []api.Model{{
		Name:                "aigate/example-model",
		ConnectionName:      "aigate",
		ModelId:             "example-model",
		ContextWindowTokens: 131072,
	}}}

	instance, err := newTestServer(Dependencies{
		Runtime:  runtime,
		APIToken: testAPIToken,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/models",
		nil,
	)
	request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Header().Get(headerRequestID))

	response := api.ModelList{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, runtime.modelList, response)
}

func TestServerListModelsRequiresAuthentication(t *testing.T) {
	t.Parallel()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(uuid.New()),
		APIToken: testAPIToken,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/models",
		nil,
	)
	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assertUnauthorizedEnvelope(t, recorder)
}
