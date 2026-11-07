package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIToken = "test-api-token"

func assertUnauthorizedEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, aichteeteapee.ErrorCodeUnauthorized, response.Code)
}

func assertErrorCode(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	want aichteeteapee.ErrorCode,
) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, want, response.Code)
}
