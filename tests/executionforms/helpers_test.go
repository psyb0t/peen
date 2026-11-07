//go:build integration

package executionforms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchURL sends one GET and returns the status code and the whole body, with
// the response body closed.
func fetchURL(
	ctx context.Context,
	client *http.Client,
	target string,
) (int, []byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		target,
		nil,
	)
	if err != nil {
		return 0, nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return 0, nil, err
	}

	return response.StatusCode, body, nil
}

func assertUnauthorized(t *testing.T, baseURL string) {
	t.Helper()

	endpoint := executionFormsWebSocketURL(t, baseURL)
	dialer := websocket.Dialer{
		Subprotocols: []string{executionFormsWebSocketProtocol},
	}
	connection, response, err := dialer.Dial(endpoint, nil)
	require.Error(t, err)
	require.Nil(t, connection)
	require.NotNil(t, response)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func sendWebSocketTurn(
	t *testing.T,
	baseURL string,
	token string,
	workspace string,
	message string,
) uuid.UUID {
	t.Helper()

	routedSession := openExecutionFormsSession(t, baseURL, token, workspace)

	dialer := websocket.Dialer{
		HandshakeTimeout: executionFormsStartupTimeout,
		Subprotocols: []string{
			executionFormsWebSocketProtocol,
			executionFormsWebSocketBearerPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(token),
			),
		},
	}
	connection, response, err := dialer.Dial(
		executionFormsWebSocketURL(t, baseURL),
		nil,
	)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	messageEvent := dabluveees.NewEvent(
		executionFormsWebSocketMessageSend,
		map[string]string{executionFormsMessageKey: message},
	).SetMetadata(
		executionFormsWebSocketMetadataSessionID,
		routedSession.String(),
	)
	require.NoError(t, connection.WriteJSON(messageEvent))

	deadline := time.Now().Add(executionFormsTurnTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))
		event := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&event))
		switch string(event.Type) {
		case executionFormsWebSocketCompleted:
			require.NotNil(t, event.Metadata)
			sessionValue, found := event.Metadata.Get(
				executionFormsWebSocketMetadataSessionID,
			)
			require.True(t, found)
			sessionText, ok := sessionValue.(string)
			require.True(t, ok)
			sessionID, parseErr := uuid.Parse(sessionText)
			require.NoError(t, parseErr)

			return sessionID
		case executionFormsWebSocketFailed:
			t.Fatalf("WebSocket message failed: %s", event.Data)
		}
	}
}

func executionFormsWebSocketURL(
	t *testing.T,
	baseURL string,
) string {
	t.Helper()

	endpoint, err := url.Parse(baseURL + executionFormsWebSocketPath)
	require.NoError(t, err)
	endpoint.Scheme = "ws"

	return endpoint.String()
}

// openExecutionFormsSession opens the process's workspace through the real
// control endpoint. Peen starts with no sessions in every execution form, so a
// turn has to name the session it belongs to.
func openExecutionFormsSession(
	t *testing.T,
	baseURL string,
	token string,
	workspace string,
) uuid.UUID {
	t.Helper()

	body, err := json.Marshal(map[string]string{"workspace": workspace})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		baseURL+executionFormsAPIOpenSessionPath,
		bytes.NewReader(body),
	)
	require.NoError(t, err)

	// A deployment with no configured token authenticates nothing, and the
	// unauthenticated scenario runs that way. Sending an empty bearer instead
	// of no header would be a malformed credential rather than none.
	if token != "" {
		request.Header.Set(
			executionFormsHeaderAuthorization,
			executionFormsBearerPrefix+token,
		)
	}

	request.Header.Set(
		executionFormsHeaderContentType,
		executionFormsContentTypeJSON,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	payload, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, response.StatusCode, string(payload))

	opened := struct {
		Session struct {
			ID        uuid.UUID `json:"id"`
			Workspace string    `json:"workspace"`
		} `json:"session"`
	}{}
	require.NoError(t, json.Unmarshal(payload, &opened))

	return opened.Session.ID
}

func assertSessionHistory(
	t *testing.T,
	baseURL string,
	token string,
	sessionID uuid.UUID,
) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+executionFormsAPIMessagesPath,
		nil,
	)
	require.NoError(t, err)
	request.Header.Set(executionFormsHeaderSessionID, sessionID.String())
	request.Header.Set(
		executionFormsHeaderAuthorization,
		executionFormsBearerPrefix+token,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	assert.Contains(t, string(body), executionFormsJSONAnswer)
}

func assertPrivateMetrics(t *testing.T, process *runningPeen) {
	t.Helper()

	publicRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		process.baseURL+executionFormsMetricsPath,
		nil,
	)
	require.NoError(t, err)
	publicRequest.Header.Set(
		executionFormsHeaderAuthorization,
		executionFormsBearerPrefix+executionFormsAPIToken,
	)
	publicResponse, err := http.DefaultClient.Do(publicRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, publicResponse.StatusCode)
	require.NoError(t, publicResponse.Body.Close())

	status, metricsBody, err := fetchURL(
		t.Context(),
		http.DefaultClient,
		process.metricsURL+executionFormsMetricsPath,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(metricsBody), executionFormsMetricFamily)
}

func assertWorkspaceFile(t *testing.T, workspace string, name string, want string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(workspace, name))
	require.NoError(t, err)
	assert.Equal(t, want, string(content))
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), executionFormsDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), executionFormsFileMode))
}
