package controlcore

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests do not call t.Parallel. Opening the durable store installs the
// generated repositories as a package default, which is process-global state.
const (
	coreTestAgentName       = "core:general-purpose"
	coreTestModelID         = "test-model"
	coreTestModelReference  = "scripted/" + coreTestModelID
	coreTestUpstreamsJSON   = `[{"name":"scripted","type":"openai"}]`
	coreTestConfigRules     = "Follow the configuration rules."
	coreTestWorkspaceRules  = "Follow the workspace rules."
	coreTestSocketDirPrefix = "pw"
	coreTestDirectoryMode   = 0o750
	coreTestFileMode        = 0o600
	coreTestStartupTimeout  = time.Minute
	coreTestShutdownTimeout = time.Minute
	coreTestClearedTimeout  = time.Second
	coreTestContextTokens   = 8192
	coreTestOutputTokens    = 1024

	coreTestDockerSocketName = "docker.sock"
)

type coreFixture struct {
	service *ControlCore
	handoff *control.Handoff
	config  peenconfig.Config
}

func newCoreFixture(t *testing.T) coreFixture {
	t.Helper()

	return newCoreFixtureWith(t, nil)
}

// newCoreFixtureWith is newCoreFixture with a hook that changes the
// deployment configuration before it is validated.
func newCoreFixtureWith(
	t *testing.T,
	mutate func(*peenconfig.Config),
) coreFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspaceDirectory := filepath.Join(root, "workspace")

	writeFixtureFile(
		t,
		filepath.Join(configDirectory, "AGENTS.md"),
		coreTestConfigRules,
	)
	writeFixtureFile(
		t,
		filepath.Join(workspaceDirectory, "AGENTS.md"),
		coreTestWorkspaceRules,
	)

	config := peenconfig.Config{
		ConfigDirectory:       configDirectory,
		StateDirectory:        filepath.Join(root, "state"),
		WorkingDirectory:      workspaceDirectory,
		WorkerSocketDirectory: shortWorkerSocketDirectory(t),

		// No Docker socket by default, so the control agent space is offered
		// whatever the machine running the test can reach. A test that wants
		// a socket sets one in mutate.
		DockerSocket:           filepath.Join(root, "no-docker.sock"),
		Agent:                  coreTestAgentName,
		UpstreamsJSON:          coreTestUpstreamsJSON,
		DefaultModel:           coreTestModelReference,
		MaxContextTokens:       coreTestContextTokens,
		CompactionMode:         peenconfig.CompactionModeDropOldest,
		CompactionOutputTokens: coreTestOutputTokens,
		CompactionTimeout:      time.Minute,
		TurnTimeout:            time.Minute,
		HTTPListenAddress:      reserveLoopbackAddress(t),
		MetricsListenAddress:   reserveLoopbackAddress(t),
	}
	// The machine running the test decides nothing about the Docker rule.
	config = config.WithDockerEnvironment("", filepath.Join(root, "no-docker.sock"))

	if mutate != nil {
		mutate(&config)
	}

	require.NoError(t, config.Validate())

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("unused"),
	).WithModels(coreTestModelID)

	handoff := control.NewHandoff()
	service := newControlCore(serviceDependencies{
		parseConfig: func() (peenconfig.Config, error) {
			return config, nil
		},
		configureAuditLog: func() error { return nil },
		auditLogDirectory: func() (string, error) { return "", nil },
		driverFactory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return driver, nil
		},
		handoff: handoff,
	})

	return coreFixture{service: service, handoff: handoff, config: config}
}

// shortWorkerSocketDirectory keeps the worker socket path inside the 107-byte
// Unix socket limit. t.TempDir() embeds the test name, which is long enough
// here that the default PEEN_CONFIG_DIR/workers root plus a session UUID would
// be refused at startup.
func shortWorkerSocketDirectory(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("", coreTestSocketDirPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })

	return directory
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), coreTestDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), coreTestFileMode))
}

// reserveLoopbackAddress returns a loopback address with a real port. Config
// validation rejects port zero, and control-core never binds these: only
// control-api does.
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

// runCoreService starts the service, waits for readiness, and returns a stop
// function that waits for a clean exit. Calling stop twice is safe.
func runCoreService(t *testing.T, fixture coreFixture) func() {
	t.Helper()

	serviceCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- fixture.service.Run(serviceCtx)
	}()

	stopped := false
	stop := func() {
		if stopped {
			return
		}

		stopped = true

		cancel()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(coreTestShutdownTimeout):
			t.Fatal("control core did not stop")
		}
	}

	t.Cleanup(stop)

	select {
	case <-fixture.service.Ready():
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("control core exited before becoming ready")
	case <-time.After(coreTestStartupTimeout):
		t.Fatal("control core did not become ready")
	}

	return stop
}

// Readiness means the durable state exists, not that a goroutine was
// scheduled, so the published core carries every dependency control-api needs.
func TestControlCorePublishesItsCoreWhenReady(t *testing.T) {
	fixture := newCoreFixture(t)
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)
	require.NotNil(t, core)

	assert.NotNil(t, core.Runtime)
	assert.NotNil(t, core.Store)
	assert.NotNil(t, core.Handle)
	assert.NotNil(t, core.Sessions)
	assert.NotNil(t, core.Metrics)
	assert.Equal(t, fixture.config.ConfigDirectory, core.Config.ConfigDirectory)
}

// Starting the controller must not create a session. One exists only after a
// client opens a workspace.
func TestControlCoreStartsWithNoSessionRows(t *testing.T) {
	fixture := newCoreFixture(t)
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	page, err := core.Store.ListSessions(
		t.Context(),
		session.ListSessionsOptions{},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// An unset PEEN_WORKSPACE_ROOT confines a client to the directory the
// controller was started in.
func TestControlCoreAppliesTheConfiguredWorkspaceRoots(t *testing.T) {
	fixture := newCoreFixture(t)
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{fixture.config.WorkingDirectory},
		core.Sessions.Roots(),
	)
}

// The controller builds its workspace policy with every directory that holds
// its data protected, including a worker socket root and an audit log
// directory set outside the state directory.
func TestControlCoreRefusesWorkspacesExposingItsDirectories(t *testing.T) {
	auditDirectory := filepath.Join(t.TempDir(), "audit")
	require.NoError(t, os.MkdirAll(auditDirectory, coreTestDirectoryMode))

	fixture := newCoreFixtureWith(t, func(config *peenconfig.Config) {
		// The system temporary directory holds the fixture's state,
		// configuration, socket root, and audit directory, the way $HOME
		// holds an installed controller's.
		config.WorkingDirectory = filepath.Dir(config.WorkerSocketDirectory)
	})
	fixture.service.dependencies.auditLogDirectory = func() (string, error) {
		return auditDirectory, nil
	}
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	for _, requested := range []string{
		fixture.config.StateDirectory,
		fixture.config.ConfigDirectory,
		filepath.Dir(fixture.config.StateDirectory),
		fixture.config.WorkerSocketDirectory,
		auditDirectory,
	} {
		_, err := core.Sessions.Open(t.Context(), requested, "")
		require.ErrorIs(t, err, commerr.ErrPermissionDenied, requested)
	}

	sibling := filepath.Join(
		filepath.Dir(fixture.config.StateDirectory),
		"workspace",
	)
	opened, err := core.Sessions.Open(t.Context(), sibling, "")
	require.NoError(t, err)
	assert.True(t, opened.Created)
}

func TestControlCoreFailsStartupWhenTheAuditDirectoryCannotBeRead(
	t *testing.T,
) {
	fixture := newCoreFixture(t)
	fixture.service.dependencies.auditLogDirectory = func() (string, error) {
		return "", ctxerrors.Wrap(commerr.ErrParseFailed, "parse audit log")
	}

	err := fixture.service.Run(t.Context())
	require.ErrorIs(t, err, commerr.ErrParseFailed)
}

// reachableDockerSocket starts a Unix socket where a Docker socket would be, so
// the controller sees the same thing a mounted socket shows it.
func reachableDockerSocket(t *testing.T) string {
	t.Helper()

	path := filepath.Join(
		shortWorkerSocketDirectory(t),
		coreTestDockerSocketName,
	)

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"unix",
		path,
	)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: coreTestClearedTimeout,
	}

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { _ = server.Close() })

	return path
}

// silentDockerSocket is a socket file that exists and never answers the Docker
// API, the shape of a socket the controller's user cannot use.
func silentDockerSocket(t *testing.T) string {
	t.Helper()

	path := filepath.Join(
		shortWorkerSocketDirectory(t),
		coreTestDockerSocketName,
	)

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"unix",
		path,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	return path
}

// A socket file the controller cannot talk to does not trigger the socket
// rule: the control agent space stays offered and the default.
func TestControlCoreIgnoresASocketThatDoesNotAnswer(t *testing.T) {
	fixture := newCoreFixtureWith(t, func(config *peenconfig.Config) {
		config.DockerSocket = silentDockerSocket(t)
	})
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	assert.Equal(t, "control", core.AgentSpaces.Default())
	assert.Contains(t, core.AgentSpaces.Names(), "control")
}

// With a Docker daemon reachable only Docker agent spaces are offered. If the
// launcher for them cannot be built, startup fails with the reason instead of
// reporting ready with no agent space that can run.
func TestControlCoreFailsStartupWhenNoAgentSpaceCanRun(t *testing.T) {
	fixture := newCoreFixtureWith(t, func(config *peenconfig.Config) {
		config.DockerSocket = reachableDockerSocket(t)
	})

	// Half an identity override passes no validation here because the config
	// is handed over directly, and the launcher cannot be built from it.
	config := fixture.config
	config.HostUsername = "peen"

	service := newControlCore(serviceDependencies{
		parseConfig: func() (peenconfig.Config, error) {
			return config, nil
		},
		configureAuditLog: func() error { return nil },
		driverFactory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return elelemtest.NewScriptedDriver(
				elelemtest.Text("unused"),
			).WithModels(coreTestModelID), nil
		},
		handoff: control.NewHandoff(),
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- service.Run(ctx) }()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PEEN_HOST_USERNAME")
		assert.Contains(t, err.Error(), "PEEN_HOST_HOME")
	case <-service.Ready():
		t.Fatal("the controller reported ready with no runnable agent space")
	case <-time.After(coreTestStartupTimeout):
		t.Fatal("the controller neither failed nor became ready")
	}
}

// The controller must not run unprotected: when it cannot mark itself
// non-dumpable it stops before it reads configuration, opens the database, or
// publishes a core for control-api to serve.
func TestControlCoreFailsStartupWhenItCannotBecomeNonDumpable(t *testing.T) {
	fixture := newCoreFixture(t)

	enablerErr := ctxerrors.Wrap(
		commerr.ErrPermissionDenied,
		"refuse the non-dumpable flag",
	)
	hasParsedConfig := false
	hasConfiguredAuditLog := false
	handoff := control.NewHandoff()

	service := newControlCore(serviceDependencies{
		enableNonDumpable: func(context.Context) error { return enablerErr },
		parseConfig: func() (peenconfig.Config, error) {
			hasParsedConfig = true

			return fixture.config, nil
		},
		configureAuditLog: func() error {
			hasConfiguredAuditLog = true

			return nil
		},
		handoff: handoff,
	})

	done := make(chan error, 1)

	go func() { done <- service.Run(t.Context()) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	case <-service.Ready():
		t.Fatal("the controller reported ready without non-dumpable")
	case <-time.After(coreTestStartupTimeout):
		t.Fatal("the controller neither failed nor became ready")
	}

	assert.False(t, hasParsedConfig, "configuration was read")
	assert.False(t, hasConfiguredAuditLog, "the audit log was configured")
	assert.NoDirExists(t, fixture.config.StateDirectory)

	select {
	case <-service.Ready():
		t.Fatal("the controller reported ready after failing")
	default:
	}

	awaitCtx, cancel := context.WithTimeout(t.Context(), coreTestClearedTimeout)
	defer cancel()

	core, err := handoff.Await(awaitCtx)
	require.Error(t, err)
	assert.Nil(t, core, "a core was published for control-api")
}

// Without a Docker socket the controller offers the control agent space and
// defaults to it.
func TestControlCoreOffersTheControlAgentSpaceWithoutADockerSocket(
	t *testing.T,
) {
	fixture := newCoreFixture(t)
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	assert.Equal(t, "control", core.AgentSpaces.Default())
	assert.Contains(t, core.AgentSpaces.Names(), "control")
	assert.Equal(t, "control", core.Sessions.AgentSpaces().Default)
}

// With a reachable Docker socket and nothing configured, the default agent
// space is the built-in Docker profile and control is not offered.
func TestControlCoreDefaultsToADockerAgentSpaceWithADockerSocket(
	t *testing.T,
) {
	fixture := newCoreFixtureWith(t, func(config *peenconfig.Config) {
		config.DockerSocket = reachableDockerSocket(t)
		config.HostUsername = "peen"
		config.HostHome = "/home/peen"
	})
	runCoreService(t, fixture)

	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)

	assert.Equal(t, "docker:default", core.AgentSpaces.Default())
	assert.NotContains(t, core.AgentSpaces.Names(), "control")
}

// Startup fails when the control agent space is the default beside a reachable
// Docker socket, even when the configuration was not validated first.
func TestControlCoreRefusesControlBesideADockerSocket(t *testing.T) {
	fixture := newCoreFixture(t)

	config := fixture.config
	config.DockerSocket = reachableDockerSocket(t)
	config.DefaultAgentSpace = "control"

	service := newControlCore(serviceDependencies{
		parseConfig: func() (peenconfig.Config, error) {
			return config, nil
		},
		configureAuditLog: func() error { return nil },
		driverFactory: func(peenconfig.Upstream) (elelem.Driver, error) {
			return elelemtest.NewScriptedDriver(
				elelemtest.Text("unused"),
			).WithModels(coreTestModelID), nil
		},
		handoff: control.NewHandoff(),
	})

	err := service.Run(t.Context())
	require.ErrorIs(t, err, peenconfig.ErrControlSpaceRefused)
}

// The controller builds its harness resolver with the deployment's disabled
// core sets, so a disabled core skill cannot be named in a message.
func TestControlCoreAppliesTheDisabledCoreSkills(t *testing.T) {
	testCases := []struct {
		name     string
		disabled string
		wantErr  bool
	}{
		{name: "nothing disabled", disabled: ""},
		{name: "planning disabled", disabled: "planning", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCoreFixtureWith(t, func(config *peenconfig.Config) {
				config.DisabledCoreSkills = tc.disabled
			})
			runCoreService(t, fixture)

			core, err := fixture.handoff.Await(t.Context())
			require.NoError(t, err)

			opened, err := core.Store.OpenWorkspace(
				t.Context(),
				fixture.config.WorkingDirectory,
				session.OpenSessionOptions{
					RootAgent: coreTestAgentName,
					ModelID:   coreTestModelReference,
				},
			)
			require.NoError(t, err)

			_, err = core.Runtime.Run(t.Context(), agent.TurnRequest{
				SessionID: &opened.Session.ID,
				Message:   "Plan this with :core:planning please.",
				Workspace: fixture.config.WorkingDirectory,
			})

			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, harness.ErrSkillNotFound)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

// Shutting down releases the published core, so a later controller in the same
// process cannot read a closed database.
func TestControlCoreClearsTheHandoffOnShutdown(t *testing.T) {
	fixture := newCoreFixture(t)
	stop := runCoreService(t, fixture)

	stop()

	awaitCtx, cancel := context.WithTimeout(
		context.Background(),
		coreTestClearedTimeout,
	)
	defer cancel()

	core, err := fixture.handoff.Await(awaitCtx)
	require.Error(t, err)
	assert.Nil(t, core)
}

// A deployment's own image wins over the build's version, which is how a local
// build runs a worker image the registry has never seen.
//
// These tests set the process-wide scope, so they stay serial with the rest of
// this file.
func TestResolveWorkerImagePrefersTheConfiguredImage(t *testing.T) {
	restore := setTestBuildVersion(t, "v1.2.3")
	defer restore()

	override, fallback := resolveWorkerImages(peenconfig.Config{
		WorkerImage: "peen-dev:local",
	})

	assert.Equal(t, "peen-dev:local", override)
	assert.Equal(t, "psyb0t/peen:v1.2.3", fallback)
}

// Without an override the build's image is only a fallback, so a profile's own
// image still applies. It tracks the controller's version verbatim.
func TestResolveWorkerImageTracksTheBuildVersionAsAFallback(t *testing.T) {
	restore := setTestBuildVersion(t, "v1.2.3")
	defer restore()

	override, fallback := resolveWorkerImages(peenconfig.Config{})

	assert.Empty(t, override)
	assert.Equal(t, "psyb0t/peen:v1.2.3", fallback)
}

// A build stamped with no version names no image, so a control-only deployment
// is not refused over an image it never uses.
func TestResolveWorkerImageWithoutAStampedVersion(t *testing.T) {
	restore := setTestBuildVersion(t, "")
	defer restore()

	override, fallback := resolveWorkerImages(peenconfig.Config{})

	assert.Empty(t, override)
	assert.Empty(t, fallback)
}

// setTestBuildVersion sets the scope key the binary stamps at startup and
// restores whatever was there before.
func setTestBuildVersion(t *testing.T, version string) func() {
	t.Helper()

	previous, had := ctxscope.GetGlobal()[buildVersionScopeKey].(string)

	if version == "" {
		ctxscope.RemoveGlobal(buildVersionScopeKey)
	} else {
		ctxscope.SetGlobal(ctxscope.Attr(buildVersionScopeKey, version))
	}

	return func() {
		if !had {
			ctxscope.RemoveGlobal(buildVersionScopeKey)

			return
		}

		ctxscope.SetGlobal(ctxscope.Attr(buildVersionScopeKey, previous))
	}
}
