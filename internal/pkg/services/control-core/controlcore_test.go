package controlcore

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests do not call t.Parallel. Opening the durable store installs the
// generated repositories as a package default, which is process-global state.
const (
	coreTestAgentName       = "default"
	coreTestModelID         = "test-model"
	coreTestModelReference  = "scripted/" + coreTestModelID
	coreTestUpstreamsJSON   = `[{"name":"scripted","type":"openai"}]`
	coreTestConfigRules     = "Follow the configuration rules."
	coreTestWorkspaceRules  = "Follow the workspace rules."
	coreTestSocketDirPrefix = "pw"
	coreTestDirectoryMode   = 0o750
	coreTestFileMode        = 0o600
	coreTestStartupTimeout  = time.Minute
	coreTestShutdownTimeout = 30 * time.Second
	coreTestClearedTimeout  = time.Second
	coreTestContextTokens   = 8192
	coreTestOutputTokens    = 1024
)

type coreFixture struct {
	service *ControlCore
	handoff *control.Handoff
	config  peenconfig.Config
}

func newCoreFixture(t *testing.T) coreFixture {
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
		ConfigDirectory:        configDirectory,
		StateDirectory:         filepath.Join(root, "state"),
		WorkingDirectory:       workspaceDirectory,
		WorkerSocketDirectory:  shortWorkerSocketDirectory(t),
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

// An unset PEEN_WORKSPACE_ROOTS confines a client to the directory the
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

	resolved := resolveWorkerImage(peenconfig.Config{
		WorkerImage: "peen-dev:local",
	})

	assert.Equal(t, "peen-dev:local", resolved)
}

// Without an override the worker image tracks the controller's own version
// verbatim, so a release runs the worker published beside it.
func TestResolveWorkerImageTracksTheBuildVersion(t *testing.T) {
	restore := setTestBuildVersion(t, "v1.2.3")
	defer restore()

	assert.Equal(
		t,
		"psyb0t/peen:v1.2.3",
		resolveWorkerImage(peenconfig.Config{}),
	)
}

// A build stamped with no version names no image, so a native-only deployment
// is not refused over an image it never uses.
func TestResolveWorkerImageWithoutAStampedVersion(t *testing.T) {
	restore := setTestBuildVersion(t, "")
	defer restore()

	assert.Empty(t, resolveWorkerImage(peenconfig.Config{}))
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
