package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	serviceTestRequestBody    = `{"message":"inspect the workspace"}`
	serviceTestConfigRules    = "Follow the configured rules."
	serviceTestWorkspaceRules = "Follow the workspace rules."
	serviceTestAgentDocument  = `---
name: default
description: scripted test agent
---
Follow the test agent rules.`
	serviceTestUpstreamsJSON  = `[{"name":"scripted","provider":"openai"}]`
	serviceTestAuthorization  = "Authorization"
	serviceTestContentType    = "Content-Type"
	serviceTestSessionID      = "X-Session-ID"
	serviceTestBearerPrefix   = "Bearer "
	serviceTestJSONMediaType  = "application/json"
	serviceTestRequestTimeout = 5 * time.Second
	// Startup opens SQLite, runs migrations and an integrity check, and
	// discovers provider models. The listener-ready channel and the polling
	// interval end every wait as soon as the service is actually up, so this
	// value is only the backstop for a loaded machine running the whole
	// race-enabled suite at once.
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
	serviceExited, err := awaitServiceStart(
		fixture.listenerReady,
		serviceDone,
	)
	serviceStopped = serviceExited
	require.NoError(t, err)

	response := awaitHTTPResponse(
		t,
		fixture.client,
		http.MethodPost,
		fixture.url("/v1/messages"),
		serviceTestRequestBody,
		"",
	)
	require.Equal(t, http.StatusOK, response.StatusCode)

	message := api.MessageResponse{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&message))
	require.NoError(t, response.Body.Close())
	assert.Equal(t, serviceTestResponse, message.Message)

	sessionID, err := uuid.Parse(response.Header.Get(serviceTestSessionID))
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, sessionID)

	pageResponse := awaitHTTPResponse(
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
	response := awaitHTTPResponse(
		t,
		fixture.client,
		http.MethodPost,
		fixture.url("/v1/messages"),
		serviceTestRequestBody,
		sessionID.String(),
	)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
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

	_, err := awaitServiceStart(fixture.listenerReady, serviceDone)
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

			return fixture.listener, nil
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

func awaitHTTPResponse(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	body string,
	sessionID string,
) *http.Response {
	t.Helper()

	var response *http.Response
	require.Eventually(t, func() bool {
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

		attemptResponse, doErr := client.Do(request)
		if doErr != nil {
			// A redirect failure can still return a non-nil response;
			// close it since this attempt is discarded either way.
			if attemptResponse != nil {
				require.NoError(t, attemptResponse.Body.Close())
			}

			return false
		}

		response = attemptResponse

		return true
	}, serviceTestStartupTimeout, serviceTestStartupInterval)

	return response
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
	listenerReady <-chan struct{},
	serviceDone <-chan error,
) (bool, error) {
	select {
	case <-listenerReady:
		return false, nil
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
	case <-time.After(serviceTestStartupTimeout):
		return false, ctxerrors.New(
			"HTTP service did not open its listener before the timeout",
		)
	}
}

type httpServiceFixture struct {
	service       *HTTPServer
	listener      net.Listener
	listenerReady chan struct{}
	driver        *elelemtest.ScriptedDriver
	client        *http.Client
	config        peenconfig.Config
}

func (f httpServiceFixture) url(path string) string {
	return "http://" + f.listener.Addr().String() + path
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
	t.Cleanup(func() {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(
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
		HTTPListenAddress:      listener.Addr().String(),
		APIToken:               serviceTestAPIToken,
	}
	require.NoError(t, config.Validate())

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text(serviceTestResponse),
	).WithModels(serviceTestModelID)
	listenerReady := make(chan struct{}, 1)
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
			_ string,
		) (net.Listener, error) {
			listenerReady <- struct{}{}

			return listener, nil
		},
	})

	return httpServiceFixture{
		service:       service,
		listener:      listener,
		listenerReady: listenerReady,
		driver:        driver,
		client:        &http.Client{Timeout: serviceTestRequestTimeout},
		config:        config,
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
