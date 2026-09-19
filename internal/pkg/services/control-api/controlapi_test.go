package controlapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/native"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests do not call t.Parallel. Opening the durable store installs the
// generated repositories as a package default, which is process-global state.
const (
	apiTestAgentName       = "default"
	apiTestModelID         = "test-model"
	apiTestModelReference  = "scripted/" + apiTestModelID
	apiTestUpstreamsJSON   = `[{"name":"scripted","provider":"openai"}]`
	apiTestToken           = "test-token"
	apiTestRules           = "Follow the workspace rules."
	apiTestSocketDirPrefix = "pw"
	apiTestDirectoryMode   = 0o750
	apiTestFileMode        = 0o600
	apiTestStartupTimeout  = 30 * time.Second
	apiTestShutdownTimeout = 30 * time.Second
	apiTestRequestTimeout  = 10 * time.Second
	apiTestContextTokens   = 8192
	apiTestOutputTokens    = 1024

	apiTestReadyPath       = "/ready"
	apiTestSessionsPath    = "/v1/sessions"
	apiTestOpenSessionPath = "/v1/sessions/open"

	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	bearerPrefix        = "Bearer "
	jsonMediaType       = "application/json"
)

type apiFixture struct {
	service *ControlAPI
	core    *control.Core
	client  *http.Client
}

func (f apiFixture) url(path string) string {
	return "http://" + f.core.Config.HTTPListenAddress + path
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspaceDirectory := filepath.Join(root, "workspace")

	writeFixtureFile(
		t,
		filepath.Join(workspaceDirectory, "AGENTS.md"),
		apiTestRules,
	)
	require.NoError(
		t,
		os.MkdirAll(configDirectory, apiTestDirectoryMode),
	)

	config := peenconfig.Config{
		ConfigDirectory:        configDirectory,
		StateDirectory:         filepath.Join(root, "state"),
		WorkingDirectory:       workspaceDirectory,
		WorkerSocketDirectory:  shortWorkerSocketDirectory(t),
		Agent:                  apiTestAgentName,
		UpstreamsJSON:          apiTestUpstreamsJSON,
		DefaultModel:           apiTestModelReference,
		MaxContextTokens:       apiTestContextTokens,
		CompactionMode:         peenconfig.CompactionModeDropOldest,
		CompactionOutputTokens: apiTestOutputTokens,
		CompactionTimeout:      time.Minute,
		TurnTimeout:            time.Minute,
		HTTPListenAddress:      reserveLoopbackAddress(t),
		MetricsListenAddress:   reserveLoopbackAddress(t),
		APIToken:               apiTestToken,
	}
	require.NoError(t, config.Validate())

	core := newTestCore(t, config)

	handoff := control.NewHandoff()
	require.NoError(t, handoff.Publish(core))

	service := newControlAPI(serviceDependencies{handoff: handoff})

	return apiFixture{
		service: service,
		core:    core,
		client:  &http.Client{Timeout: apiTestRequestTimeout},
	}
}

// newTestCore builds the same Core control-core publishes, without importing
// that service: a service never imports a sibling service.
func newTestCore(t *testing.T, config peenconfig.Config) *control.Core {
	t.Helper()

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("unused"),
	).WithModels(apiTestModelID)

	models, err := agent.NewRegistry(t.Context(), agent.RegistryOptions{
		Upstreams: []peenconfig.Upstream{
			{Name: "scripted", Provider: peenconfig.ProviderTypeOpenAI},
		},
		DefaultModel:     config.DefaultModel,
		MaxContextTokens: config.MaxContextTokens,
		Factory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return driver, nil
		},
	})
	require.NoError(t, err)

	assembled, err := agent.Assemble(t.Context(), agent.AssembleOptions{
		StateDirectory: config.StateDirectory,
		Runtime: agent.RuntimeOptions{
			Models:           models,
			RootAgent:        config.Agent,
			DefaultModel:     config.DefaultModel,
			MaxContextTokens: config.MaxContextTokens,
			TurnTimeout:      config.TurnTimeout,
			CompactionMode:   config.CompactionMode,
			ConfigDirectory:  config.ConfigDirectory,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, assembled.Handle.Close())
	})

	policy, err := control.NewWorkspacePolicy(
		[]string{config.WorkingDirectory},
	)
	require.NoError(t, err)

	profiles, err := config.ExecutionProfiles()
	require.NoError(t, err)

	// The relay is the supervisor's publisher here exactly as it is in
	// control-core, so the service under test registers its delivery path on
	// the same object a worker would write through.
	relay := control.NewEventRelay()

	nativeLauncher, err := native.New(native.Options{})
	require.NoError(t, err)

	workers, err := supervisor.New(supervisor.Options{
		Store:    assembled.Store,
		Profiles: profiles,
		Launchers: map[worker.Kind]worker.Launcher{
			worker.KindNative: nativeLauncher,
		},
		Publisher:       relay,
		SocketRoot:      config.WorkerSocketRoot(),
		ConfigDirectory: config.ConfigDirectory,
	})
	require.NoError(t, err)

	sessions, err := control.NewRegistry(control.RegistryOptions{
		Store:        assembled.Store,
		Policy:       policy,
		Profiles:     profiles,
		RootAgent:    config.Agent,
		DefaultModel: config.DefaultModel,
		Workers:      workers,
	})
	require.NoError(t, err)

	turns, err := control.NewTurnRouter(sessions, workers)
	require.NoError(t, err)

	return &control.Core{
		Config:   config,
		Runtime:  assembled.Runtime,
		Store:    assembled.Store,
		Handle:   assembled.Handle,
		Sessions: sessions,
		Profiles: profiles,
		Workers:  workers,
		Turns:    turns,
		Events:   relay,
		Metrics:  metrics.New(),
	}
}

// shortWorkerSocketDirectory keeps the worker socket path inside the 107-byte
// Unix socket limit. t.TempDir() embeds the test name, which is long enough
// here that the default PEEN_CONFIG_DIR/workers root plus a session UUID would
// be refused at startup.
func shortWorkerSocketDirectory(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("", apiTestSocketDirPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })

	return directory
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), apiTestDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), apiTestFileMode))
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"tcp",
		"127.0.0.1:0",
	)
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	return address
}

func runAPIService(t *testing.T, fixture apiFixture) {
	t.Helper()

	serviceCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- fixture.service.Run(serviceCtx)
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case <-done:
		case <-time.After(apiTestShutdownTimeout):
			t.Fatal("control API did not stop")
		}
	})

	select {
	case <-fixture.service.Ready():
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("control API exited before becoming ready")
	case <-time.After(apiTestStartupTimeout):
		t.Fatal("control API did not become ready")
	}
}

func (f apiFixture) request(
	t *testing.T,
	method string,
	path string,
	body string,
) *http.Response {
	t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	var request *http.Request

	var err error

	if reader == nil {
		request, err = http.NewRequestWithContext(
			t.Context(),
			method,
			f.url(path),
			nil,
		)
	} else {
		request, err = http.NewRequestWithContext(
			t.Context(),
			method,
			f.url(path),
			reader,
		)
		request.Header.Set(headerContentType, jsonMediaType)
	}

	require.NoError(t, err)
	request.Header.Set(headerAuthorization, bearerPrefix+apiTestToken)

	response, err := f.client.Do(request)
	require.NoError(t, err)

	return response
}

// The service declares its dependency so servicepack launches control-core
// first. Without this edge the API could start against no durable state.
func TestControlAPIDependsOnControlCore(t *testing.T) {
	t.Parallel()

	service := newControlAPI(serviceDependencies{
		handoff: control.NewHandoff(),
	})

	assert.Equal(t, []string{control.CoreServiceName}, service.Dependencies())
}

// Readiness means the configured endpoint answers a connection, so a client
// that waits for it can send a request immediately.
func TestControlAPIBecomesReadyOnlyWhenItsEndpointAnswers(t *testing.T) {
	fixture := newAPIFixture(t)
	runAPIService(t, fixture)

	response := fixture.request(t, http.MethodGet, apiTestReadyPath, "")
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

// The API serves the state control-core published: it starts with no sessions
// and creates one only when a client opens a workspace.
func TestControlAPIServesTheSessionControlSurface(t *testing.T) {
	fixture := newAPIFixture(t)
	runAPIService(t, fixture)

	listed := fixture.request(t, http.MethodGet, apiTestSessionsPath, "")
	page := api.SessionPage{}
	require.NoError(t, json.NewDecoder(listed.Body).Decode(&page))
	require.NoError(t, listed.Body.Close())
	assert.Empty(t, page.Items)

	body, err := json.Marshal(api.OpenSessionRequest{
		Workspace: fixture.core.Config.WorkingDirectory,
	})
	require.NoError(t, err)

	opened := fixture.request(
		t,
		http.MethodPost,
		apiTestOpenSessionPath,
		string(body),
	)

	result := api.OpenedSession{}
	require.NoError(t, json.NewDecoder(opened.Body).Decode(&result))
	require.NoError(t, opened.Body.Close())
	require.Equal(t, http.StatusOK, opened.StatusCode)

	assert.True(t, result.Created)
	assert.Equal(
		t,
		fixture.core.Config.WorkingDirectory,
		result.Session.Workspace,
	)

	stored, err := fixture.core.Store.ListSessions(
		t.Context(),
		session.ListSessionsOptions{},
	)
	require.NoError(t, err)
	require.Len(t, stored.Items, 1)
	assert.Equal(t, result.Session.Id, stored.Items[0].ID)
}

// A workspace outside every configured root is refused and creates nothing.
func TestControlAPIRefusesAWorkspaceOutsideItsRoots(t *testing.T) {
	fixture := newAPIFixture(t)
	runAPIService(t, fixture)

	body, err := json.Marshal(api.OpenSessionRequest{Workspace: t.TempDir()})
	require.NoError(t, err)

	response := fixture.request(
		t,
		http.MethodPost,
		apiTestOpenSessionPath,
		string(body),
	)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusForbidden, response.StatusCode)

	stored, err := fixture.core.Store.ListSessions(
		t.Context(),
		session.ListSessionsOptions{},
	)
	require.NoError(t, err)
	assert.Empty(t, stored.Items)
}

// Without a published core the API has nothing to serve, so it fails instead of
// listening on empty state.
func TestControlAPIFailsWhenNoCoreIsPublished(t *testing.T) {
	t.Parallel()

	service := newControlAPI(serviceDependencies{
		handoff: control.NewHandoff(),
	})

	runCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := service.Run(runCtx)
	require.Error(t, err)
}
