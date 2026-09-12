package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/harness"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	serviceTestAgentName      = "default"
	serviceTestModelReference = "scripted/test-model"
	serviceTestModelID        = "test-model"
	serviceTestAPIToken       = "test-api-token"
	serviceTestMessage        = "inspect the workspace"
	serviceTestResponse       = "inspection complete"
	serviceTestConfigRules    = "Follow the configured rules."
	serviceTestWorkspaceRules = "Follow the workspace rules."
	serviceTestAgentDocument  = `---
name: default
description: scripted test agent
---
Follow the test agent rules.`
	serviceTestUpstreamsJSON      = `[{"name":"scripted","provider":"openai"}]`
	serviceTestAuthorization      = "Authorization"
	serviceTestContentType        = "Content-Type"
	serviceTestSessionID          = "X-Session-ID"
	serviceTestBearerPrefix       = "Bearer "
	serviceTestJSONMediaType      = "application/json"
	serviceTestMetricsPath        = "/metrics"
	serviceTestReadyPath          = "/ready"
	serviceTestRequestTimeout     = 60 * time.Second
	serviceTestWebSocketPath      = "/v1/ws"
	serviceTestWebSocketSessionID = "sessionId"
	serviceTestWebSocketProtocol  = "peen.v1"
	//nolint:gosec // This is the non-secret WebSocket subprotocol namespace.
	serviceTestWebSocketBearerPrefix = "peen.bearer."
	serviceTestWebSocketMessageSend  = "message.send"
	serviceTestWebSocketCompleted    = "message.completed"
	serviceTestWebSocketFailed       = "message.failed"
	// Startup opens SQLite, runs migrations and an integrity check, and
	// discovers provider models. The readiness probe and polling interval end
	// every wait as soon as the service is actually up. The timeout is only the
	// backstop for a loaded machine running the whole
	// complete unit suite at once.
	serviceTestStartupTimeout  = 60 * time.Second
	serviceTestStartupInterval = 10 * time.Millisecond
	serviceTestFileMode        = 0o600
	serviceTestDirectoryMode   = 0o700

	serviceTestSecretAPIToken      = "very-secret-http-api-token"
	serviceTestSecretUpstreamKey   = "very-secret-upstream-provider-key"
	serviceTestUpstreamKeyEnvVar   = "PEEN_TEST_LOG_UPSTREAM_KEY"
	serviceTestLogUpstreamName     = "aigate"
	serviceTestLogConfigDirectory  = "/data/peen"
	serviceTestLogWorkingDirectory = "/workspace"
)

func TestHTTPServiceRunsRealSQLiteAndAPI(t *testing.T) {
	fixture := newHTTPServiceFixture(t)
	serviceContext, cancel := context.WithCancel(context.Background())
	serviceDone := make(chan error, 1)
	serviceStopped := false
	t.Cleanup(func() {
		if serviceStopped {
			return
		}

		cancel()
		require.NoError(t, awaitServiceStop(t, serviceDone))
	})
	go func() {
		serviceDone <- fixture.service.Run(serviceContext)
	}()
	serviceExited, err := awaitServiceStart(t, fixture, serviceDone)
	serviceStopped = serviceExited
	require.NoError(t, err)

	sessionID := sendWebSocketTurn(
		t,
		fixture,
		uuid.Nil,
		serviceTestMessage,
	)

	pageResponse := sendHTTPResponse(
		t,
		fixture.client,
		http.MethodGet,
		fixture.url("/v1/messages?limit=10&offset=0&order=asc"),
		"",
		sessionID.String(),
	)
	require.Equal(t, http.StatusOK, pageResponse.StatusCode)

	page := api.MessagePage{}
	require.NoError(t, json.NewDecoder(pageResponse.Body).Decode(&page))
	require.NoError(t, pageResponse.Body.Close())
	assert.Equal(t, []string{serviceTestMessage, serviceTestResponse}, messageContents(page.Items))
	assert.False(t, page.HasMore)
	assert.Len(t, fixture.driver.Requests(), 1)

	publicMetrics := sendHTTPResponse(
		t,
		fixture.client,
		http.MethodGet,
		fixture.url(serviceTestMetricsPath),
		"",
		"",
	)
	require.Equal(t, http.StatusNotFound, publicMetrics.StatusCode)
	require.NoError(t, publicMetrics.Body.Close())

	metricsRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		fixture.metricsURL(serviceTestMetricsPath),
		nil,
	)
	require.NoError(t, err)

	metricsResponse, err := fixture.client.Do(metricsRequest)
	require.NoError(t, err)
	metricsBody, readErr := io.ReadAll(metricsResponse.Body)
	closeErr := metricsResponse.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, metricsResponse.StatusCode)
	assert.Contains(t, string(metricsBody), "peen_http_requests_total")

	cancel()
	require.NoError(t, awaitServiceStop(t, serviceDone))
	serviceStopped = true
}

// PEEN_WORKING_DIR is documented as the directory the binary changes to before
// constructing the runtime, not merely a default workspace string. Without the
// chdir every relative path resolved anywhere else, including inside a command
// the agent spawns, lands wherever the process happened to start.
func TestHTTPServiceEntersTheConfiguredWorkingDirectory(t *testing.T) {
	fixture := newHTTPServiceFixture(t)

	want, err := filepath.EvalSymlinks(fixture.config.WorkingDirectory)
	require.NoError(t, err)

	runServiceUntilReady(t, fixture)

	current, err := os.Getwd()
	require.NoError(t, err)

	resolved, err := filepath.EvalSymlinks(current)
	require.NoError(t, err)
	assert.Equal(t, want, resolved)
}

// A turn left running by a killed process is excluded from completed history
// forever, so its user message disappears from every later prompt. Startup has
// to settle those before it serves anything.
func TestHTTPServiceRecoversInterruptedTurnsBeforeServing(t *testing.T) {
	fixture := newHTTPServiceFixture(t)
	sessionID, turnID := seedInterruptedTurn(t, fixture.config)

	runServiceUntilReady(t, fixture)

	state := turnStateForSession(t, fixture.config, sessionID, turnID)
	assert.Equal(t, models.TurnStateInterrupted, state)

	// The recovered session must accept a new turn. Before recovery the
	// orphaned row stayed running and nothing in memory knew about it.
	_ = sendWebSocketTurn(
		t,
		fixture,
		sessionID,
		serviceTestMessage,
	)
}

// runServiceUntilReady starts the service and stops it during cleanup.
func runServiceUntilReady(t *testing.T, fixture httpServiceFixture) {
	t.Helper()

	serviceContext, cancel := context.WithCancel(context.Background())
	serviceDone := make(chan error, 1)

	go func() {
		serviceDone <- fixture.service.Run(serviceContext)
	}()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, awaitServiceStop(t, serviceDone))
	})

	_, err := awaitServiceStart(t, fixture, serviceDone)
	require.NoError(t, err)
}

// seedInterruptedTurn writes a session holding a turn still marked running,
// exactly what a killed process leaves behind.
func seedInterruptedTurn(
	t *testing.T,
	config peenconfig.Config,
) (uuid.UUID, uuid.UUID) {
	t.Helper()

	handle, err := db.Open(
		t.Context(),
		db.Config{Directory: config.ConfigDirectory},
	)
	require.NoError(t, err)

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	opened, err := store.CreateOrResume(t.Context(), nil, session.OpenSessionOptions{
		RootAgent: serviceTestAgentName,
		ModelID:   serviceTestModelReference,
	})
	require.NoError(t, err)

	lease, err := store.AcquireTurn(
		t.Context(),
		opened.Session.ID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: config.WorkingDirectory,
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleUser,
				Content: serviceTestMessage,
			}},
		},
	)
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	return opened.Session.ID, lease.TurnID
}

func turnStateForSession(
	t *testing.T,
	config peenconfig.Config,
	sessionID uuid.UUID,
	turnID uuid.UUID,
) models.TurnState {
	t.Helper()

	handle, err := db.Open(
		t.Context(),
		db.Config{Directory: config.ConfigDirectory},
	)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	query := repositories.Use(handle.GormDB)

	turn, err := query.Turn.WithContext(t.Context()).
		Where(query.Turn.ID.Eq(turnID), query.Turn.SessionID.Eq(sessionID)).
		First()
	require.NoError(t, err)

	return turn.State
}

func TestHTTPServiceRejectsUnavailableModelBeforeOpeningListener(t *testing.T) {
	fixture := newHTTPServiceFixture(t)
	listenerOpened := false
	fixture.service = newHTTPServer(serviceDependencies{
		parseConfig: func() (peenconfig.Config, error) {
			return fixture.config, nil
		},
		driverFactory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return elelemtest.NewScriptedDriver().WithModels("other-model"), nil
		},
		listen: func(
			_ context.Context,
			_ string,
			_ string,
		) (net.Listener, error) {
			listenerOpened = true

			return nil, ctxerrors.New("unexpected listener creation")
		},
	})

	err := fixture.service.Run(context.Background())

	require.ErrorIs(t, err, agent.ErrModelUnavailable)
	assert.False(t, listenerOpened)
}

// TestLogValidatedConfigRedactsSecrets replaces the process-global slog
// default, so it must not run in parallel with anything else that reads or
// sets it.
func TestLogValidatedConfigRedactsSecrets(t *testing.T) {
	t.Setenv(serviceTestUpstreamKeyEnvVar, serviceTestSecretUpstreamKey)

	var captured bytes.Buffer

	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	config := peenconfig.Config{
		ConfigDirectory:   serviceTestLogConfigDirectory,
		WorkingDirectory:  serviceTestLogWorkingDirectory,
		Agent:             serviceTestAgentName,
		DefaultModel:      serviceTestModelReference,
		HTTPListenAddress: ":8080",
		APIToken:          serviceTestSecretAPIToken,
	}
	upstreams := []peenconfig.Upstream{
		{
			Name:      serviceTestLogUpstreamName,
			Provider:  peenconfig.ProviderTypeOpenAI,
			APIKeyEnv: serviceTestUpstreamKeyEnvVar,
		},
	}

	logValidatedConfig(context.Background(), config, upstreams)

	output := captured.String()
	assert.NotContains(t, output, serviceTestSecretAPIToken)
	assert.NotContains(t, output, serviceTestSecretUpstreamKey)
	assert.Contains(t, output, `"validated configuration"`)
	assert.Contains(t, output, `"api_token_configured":true`)
	assert.Contains(t, output, `"provider_count":1`)
	assert.Contains(t, output, `"`+serviceTestLogUpstreamName+`"`)
}

func sendHTTPResponse(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	body string,
	sessionID string,
) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		method,
		url,
		strings.NewReader(body),
	)
	require.NoError(t, err)
	request.Header.Set(
		serviceTestAuthorization,
		serviceTestBearerPrefix+serviceTestAPIToken,
	)
	request.Header.Set(serviceTestContentType, serviceTestJSONMediaType)
	if sessionID != "" {
		request.Header.Set(serviceTestSessionID, sessionID)
	}

	response, err := client.Do(request)
	require.NoError(t, err)

	return response
}

func sendWebSocketTurn(
	t *testing.T,
	fixture httpServiceFixture,
	sessionID uuid.UUID,
	message string,
) uuid.UUID {
	t.Helper()
	if sessionID == uuid.Nil {
		sessionID = uuid.New()
	}

	endpoint, err := url.Parse(fixture.url(serviceTestWebSocketPath))
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	query := endpoint.Query()
	query.Set(serviceTestWebSocketSessionID, sessionID.String())
	endpoint.RawQuery = query.Encode()

	dialer := websocket.Dialer{
		HandshakeTimeout: serviceTestRequestTimeout,
		Subprotocols: []string{
			serviceTestWebSocketProtocol,
			serviceTestWebSocketBearerPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(serviceTestAPIToken),
			),
		},
	}
	connection, response, err := dialer.Dial(endpoint.String(), nil)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	require.NoError(t, connection.WriteJSON(dabluveees.NewEvent(
		serviceTestWebSocketMessageSend,
		map[string]string{"message": message},
	)))
	deadline := time.Now().Add(serviceTestRequestTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))
		event := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&event))
		switch event.Type {
		case serviceTestWebSocketCompleted:
			result := struct {
				Queued bool `json:"queued"`
			}{}
			require.NoError(t, json.Unmarshal(event.Data, &result))
			require.False(t, result.Queued)

			return sessionID
		case serviceTestWebSocketFailed:
			t.Fatalf("WebSocket message failed: %s", event.Data)
		case dabluveees.EventTypeSystemLog,
			dabluveees.EventTypeShellExec,
			dabluveees.EventTypeEchoRequest,
			dabluveees.EventTypeEchoReply,
			dabluveees.EventTypeError:
			continue
		default:
			continue
		}
	}
}

func awaitServiceStop(t *testing.T, serviceDone <-chan error) error {
	t.Helper()

	var runErr error
	require.Eventually(t, func() bool {
		select {
		case runErr = <-serviceDone:
			return true
		default:
			return false
		}
	}, serviceTestStartupTimeout, serviceTestStartupInterval)

	return runErr
}

func awaitServiceStart(
	t *testing.T,
	fixture httpServiceFixture,
	serviceDone <-chan error,
) (bool, error) {
	t.Helper()

	timeout := time.NewTimer(serviceTestStartupTimeout)
	defer timeout.Stop()

	for {
		request, requestErr := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			fixture.url(serviceTestReadyPath),
			nil,
		)
		if requestErr != nil {
			return false, ctxerrors.Wrap(
				requestErr,
				"create HTTP service readiness request",
			)
		}

		response, requestErr := fixture.client.Do(request)
		if requestErr == nil {
			statusCode := response.StatusCode
			closeErr := response.Body.Close()
			if statusCode == http.StatusOK && closeErr == nil {
				return false, nil
			}
		}

		select {
		case runErr := <-serviceDone:
			if runErr == nil {
				return true, ctxerrors.New(
					"HTTP service stopped before opening its listener",
				)
			}

			return true, ctxerrors.Wrap(
				runErr,
				"HTTP service stopped before opening its listener",
			)
		case <-timeout.C:
			return false, ctxerrors.New(
				"HTTP service did not open its listener before the timeout",
			)
		case <-time.After(serviceTestStartupInterval):
		}
	}
}

type httpServiceFixture struct {
	service         *HTTPServer
	metricsListener net.Listener
	httpAddress     string
	driver          *elelemtest.ScriptedDriver
	client          *http.Client
	config          peenconfig.Config
}

func (f httpServiceFixture) url(path string) string {
	return "http://" + f.httpAddress + path
}

func (f httpServiceFixture) metricsURL(path string) string {
	return "http://" + f.metricsListener.Addr().String() + path
}

func newHTTPServiceFixture(t *testing.T) httpServiceFixture {
	t.Helper()

	// Run changes the process directory, which is global to the test binary.
	// Restoring it keeps one service test from relocating the next one.
	originalDirectory, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalDirectory))
	})

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspaceDirectory := filepath.Join(root, "workspace")
	writeHTTPServiceFixtureFile(
		t,
		filepath.Join(configDirectory, "AGENTS.md"),
		serviceTestConfigRules,
	)
	writeHTTPServiceFixtureFile(
		t,
		filepath.Join(workspaceDirectory, "AGENTS.md"),
		serviceTestWorkspaceRules,
	)
	writeHTTPServiceFixtureFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", serviceTestAgentName+".md"),
		serviceTestAgentDocument,
	)

	listener, listenErr := (&net.ListenConfig{}).
		Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, listenErr)
	httpAddress := listener.Addr().String()
	require.NoError(t, listener.Close())
	metricsListener, metricsListenErr := (&net.ListenConfig{}).
		Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, metricsListenErr)
	t.Cleanup(func() {
		if closeErr := metricsListener.Close(); closeErr != nil && !errors.Is(
			closeErr,
			net.ErrClosed,
		) {
			require.NoError(t, closeErr)
		}
	})

	config := peenconfig.Config{
		ConfigDirectory:        configDirectory,
		WorkingDirectory:       workspaceDirectory,
		Agent:                  serviceTestAgentName,
		UpstreamsJSON:          serviceTestUpstreamsJSON,
		DefaultModel:           serviceTestModelReference,
		MaxContextTokens:       8192,
		CompactionMode:         peenconfig.CompactionModeDropOldest,
		CompactionOutputTokens: 1024,
		CompactionTimeout:      time.Minute,
		TurnTimeout:            time.Minute,
		HTTPListenAddress:      httpAddress,
		MetricsListenAddress:   metricsListener.Addr().String(),
		APIToken:               serviceTestAPIToken,
	}
	require.NoError(t, config.Validate())

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text(serviceTestResponse),
	).WithModels(serviceTestModelID)
	service := newHTTPServer(serviceDependencies{
		parseConfig: func() (peenconfig.Config, error) {
			return config, nil
		},
		driverFactory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return driver, nil
		},
		listen: func(
			_ context.Context,
			_ string,
			address string,
		) (net.Listener, error) {
			if address != config.MetricsListenAddress {
				return nil, ctxerrors.New("unexpected listener address")
			}

			return metricsListener, nil
		},
	})

	return httpServiceFixture{
		service:         service,
		metricsListener: metricsListener,
		httpAddress:     httpAddress,
		driver:          driver,
		client:          &http.Client{Timeout: serviceTestRequestTimeout},
		config:          config,
	}
}

func writeHTTPServiceFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), serviceTestDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), serviceTestFileMode))
}

func messageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}

// An inline agent definition must never carry instructions the same
// deployment would refuse to read from a stored agent file. The two bounds
// come from different places, so equal defaults are not enough on their own.
func TestAdHocInstructionBytesNeverExceedsTheStoredFileBound(t *testing.T) {
	t.Parallel()

	storedFileBytes := int(harness.DefaultLimits().MaxFileBytes)

	testCases := []struct {
		name       string
		configured int
		want       int
	}{
		{
			name:       "below the stored file bound is kept",
			configured: storedFileBytes / 2,
			want:       storedFileBytes / 2,
		},
		{
			name:       "equal to the stored file bound is kept",
			configured: storedFileBytes,
			want:       storedFileBytes,
		},
		{
			name:       "above the stored file bound is clamped",
			configured: storedFileBytes * 2,
			want:       storedFileBytes,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := adHocInstructionBytes(peenconfig.Config{
				MaxAdHocAgentInstructionBytes: tc.configured,
			})
			assert.Equal(t, tc.want, got)
		})
	}
}
