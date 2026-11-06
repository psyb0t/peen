package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openSystemSessionRecorder(
	t *testing.T,
	registry *testSessionRegistry,
) *httptest.ResponseRecorder {
	t.Helper()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(uuid.New()),
		Sessions: registry,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			apiBaseURL+"/sessions/system",
			nil,
		),
	)

	return recorder
}

func TestServerOpensTheSystemSession(t *testing.T) {
	sessionID := uuid.New()

	recorder := openSystemSessionRecorder(t, &testSessionRegistry{
		opened: testOpenedSession(sessionID, true),
	})
	require.Equal(t, http.StatusOK, recorder.Code)

	var opened api.OpenedSession
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &opened))
	assert.Equal(t, sessionID, opened.Session.Id)
	assert.True(t, opened.Created)
}

func TestServerRefusesTheSystemSessionWhileItIsOff(t *testing.T) {
	recorder := openSystemSessionRecorder(t, &testSessionRegistry{
		openErr: ctxerrors.Wrap(commerr.ErrPermissionDenied, "off"),
	})
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var failure api.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &failure))
	assert.Equal(t, ErrorCodeSystemAgentOff, failure.Code)
}

func TestMessageSendSystemCannotCombineWithTo(t *testing.T) {
	system := true
	target := uuid.NewString()

	_, err := messageSendDataToRequest(wsapi.MessageSendData{
		Message: "hello",
		System:  &system,
		To:      &target,
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestMessageSendCarriesSystem(t *testing.T) {
	system := true

	request, err := messageSendDataToRequest(wsapi.MessageSendData{
		Message: "hello",
		System:  &system,
	})
	require.NoError(t, err)
	assert.True(t, request.System)

	ordinary, err := messageSendDataToRequest(wsapi.MessageSendData{
		Message: "hello",
	})
	require.NoError(t, err)
	assert.False(t, ordinary.System)
}
