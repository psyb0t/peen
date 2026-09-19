package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	stateTestConfigDirectory = "/srv/peen/config"
	stateTestStateDirectory  = "/srv/peen/state"
	stateTestWorkspace       = "/srv/work/project"
	stateTestUpstreamsJSON   = `[{"name":"scripted","provider":"openai"}]`
	stateTestModelReference  = "scripted/test-model"
	stateTestContextTokens   = 8192
	stateTestOutputTokens    = 1024
	stateTestDirMode         = 0o700
)

func stateTestConfig() config.Config {
	return config.Config{
		ConfigDirectory:        stateTestConfigDirectory,
		StateDirectory:         stateTestStateDirectory,
		WorkingDirectory:       stateTestWorkspace,
		Agent:                  "default",
		UpstreamsJSON:          stateTestUpstreamsJSON,
		DefaultModel:           stateTestModelReference,
		MaxContextTokens:       stateTestContextTokens,
		CompactionMode:         config.CompactionModeDropOldest,
		CompactionOutputTokens: stateTestOutputTokens,
		CompactionTimeout:      time.Minute,
		TurnTimeout:            time.Minute,
		HTTPListenAddress:      ":8080",
		MetricsListenAddress:   "127.0.0.1:9090",
	}
}

// PEEN_CONFIG_DIR is mounted into every Docker worker read-only. Controller
// state inside it, or the two directories being the same path, would carry
// SQLite and the audit logs into every worker, so the arrangement is refused at
// startup rather than discovered from a worker reading another session's
// transcript.
func TestConfigRefusesStateInsideTheWorkerVisibleConfigDirectory(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		config    string
		state     string
		wantValid bool
	}{
		{
			name:      "separate directories",
			config:    stateTestConfigDirectory,
			state:     stateTestStateDirectory,
			wantValid: true,
		},
		{
			name:   "identical directories",
			config: stateTestConfigDirectory,
			state:  stateTestConfigDirectory,
		},
		{
			name:   "identical after cleaning",
			config: stateTestConfigDirectory,
			state:  stateTestConfigDirectory + "/",
		},
		{
			name:   "state directly inside config",
			config: stateTestConfigDirectory,
			state:  filepath.Join(stateTestConfigDirectory, "state"),
		},
		{
			name:   "state deeper inside config",
			config: stateTestConfigDirectory,
			state:  filepath.Join(stateTestConfigDirectory, "a", "b", "state"),
		},
		{
			name:   "config inside state",
			config: filepath.Join(stateTestStateDirectory, "config"),
			state:  stateTestStateDirectory,
		},
		{
			name:      "sibling sharing a name prefix",
			config:    "/srv/peen",
			state:     "/srv/peen-state",
			wantValid: true,
		},
		{
			name:   "config at the filesystem root",
			config: "/",
			state:  stateTestStateDirectory,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deployment := stateTestConfig()
			deployment.ConfigDirectory = tc.config
			deployment.StateDirectory = tc.state

			err := deployment.Validate()

			if tc.wantValid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, config.ErrInvalidConfig)
		})
	}
}

// Both directories are absolute, because a worker mount and a database path
// cannot be resolved against whatever directory the controller happened to
// start in.
func TestConfigRequiresBothDirectoriesAbsolute(t *testing.T) {
	t.Parallel()

	missingState := stateTestConfig()
	missingState.StateDirectory = ""
	require.ErrorIs(t, missingState.Validate(), config.ErrInvalidConfig)

	relativeState := stateTestConfig()
	relativeState.StateDirectory = "state"
	require.ErrorIs(t, relativeState.Validate(), config.ErrInvalidConfig)

	relativeConfig := stateTestConfig()
	relativeConfig.ConfigDirectory = "config"
	require.ErrorIs(t, relativeConfig.Validate(), config.ErrInvalidConfig)
}

// The worker socket root is controller runtime state, so it defaults under the
// state directory. A root under the configuration directory would be inside the
// mount every worker receives.
func TestWorkerSocketRootDefaultsUnderTheStateDirectory(t *testing.T) {
	t.Parallel()

	deployment := stateTestConfig()

	root := deployment.WorkerSocketRoot()

	assert.Equal(
		t,
		filepath.Join(stateTestStateDirectory, "workers"),
		root,
	)
	assert.NotEqual(t, stateTestConfigDirectory, root)
	assert.False(
		t,
		isInside(stateTestConfigDirectory, root),
		"the socket root must not sit inside the worker-visible config mount",
	)
}

func TestWorkerSocketRootHonoursAnExplicitDirectory(t *testing.T) {
	t.Parallel()

	deployment := stateTestConfig()
	deployment.WorkerSocketDirectory = "/run/peen/workers"

	assert.Equal(t, "/run/peen/workers", deployment.WorkerSocketRoot())
}

// A string comparison alone is bypassable: a state directory whose path shares
// no prefix with the configuration directory can still be a symlink into it,
// and the read-only configuration mount would then carry SQLite into every
// worker. Both directions are checked, because either nesting puts controller
// state inside the directory a worker receives.
func TestConfigRefusesASymlinkedStateDirectory(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		// build returns the config and state paths to validate, after creating
		// whatever real directories and links the case needs.
		build func(t *testing.T, root string) (string, string)
		valid bool
	}{
		{
			name: "state is a link into the config directory",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				config := filepath.Join(root, "config")
				inside := filepath.Join(config, "hidden-state")
				link := filepath.Join(root, "state")

				require.NoError(t, os.MkdirAll(inside, stateTestDirMode))
				require.NoError(t, os.Symlink(inside, link))

				return config, link
			},
		},
		{
			name: "state sits under a linked parent that lands in config",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				config := filepath.Join(root, "config")
				link := filepath.Join(root, "linked-parent")

				require.NoError(t, os.MkdirAll(config, stateTestDirMode))
				require.NoError(t, os.Symlink(config, link))

				// The state directory itself does not exist yet, which is the
				// ordinary first-run case. Its parent is the link.
				return config, filepath.Join(link, "state")
			},
		},
		{
			name: "config is a link into the state directory",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				state := filepath.Join(root, "state")
				inside := filepath.Join(state, "hidden-config")
				link := filepath.Join(root, "config")

				require.NoError(t, os.MkdirAll(inside, stateTestDirMode))
				require.NoError(t, os.Symlink(inside, link))

				return link, state
			},
		},
		{
			name: "both links resolve to the same directory",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				shared := filepath.Join(root, "shared")
				configLink := filepath.Join(root, "config")
				stateLink := filepath.Join(root, "state")

				require.NoError(t, os.MkdirAll(shared, stateTestDirMode))
				require.NoError(t, os.Symlink(shared, configLink))
				require.NoError(t, os.Symlink(shared, stateLink))

				return configLink, stateLink
			},
		},
		{
			name: "links to genuinely separate directories are allowed",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				config := filepath.Join(root, "real-config")
				state := filepath.Join(root, "real-state")
				configLink := filepath.Join(root, "config")
				stateLink := filepath.Join(root, "state")

				require.NoError(t, os.MkdirAll(config, stateTestDirMode))
				require.NoError(t, os.MkdirAll(state, stateTestDirMode))
				require.NoError(t, os.Symlink(config, configLink))
				require.NoError(t, os.Symlink(state, stateLink))

				return configLink, stateLink
			},
			valid: true,
		},
		{
			name: "neither directory exists yet",
			build: func(t *testing.T, root string) (string, string) {
				t.Helper()

				return filepath.Join(root, "config"),
					filepath.Join(root, "state")
			},
			valid: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			configDirectory, stateDirectory := tc.build(t, root)

			deployment := stateTestConfig()
			deployment.ConfigDirectory = configDirectory
			deployment.StateDirectory = stateDirectory

			err := deployment.Validate()

			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, config.ErrInvalidConfig)
		})
	}
}

// A path that cannot be resolved refuses the deployment rather than falling
// back to the literal string, because an unreadable component is exactly where
// a link could hide.
func TestConfigRefusesAnUnresolvableDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(config, stateTestDirMode))

	// A link pointing at itself cannot be resolved, and the kernel reports a
	// loop rather than a missing file.
	looping := filepath.Join(root, "loop")
	require.NoError(t, os.Symlink(looping, looping))

	deployment := stateTestConfig()
	deployment.ConfigDirectory = config
	deployment.StateDirectory = filepath.Join(looping, "state")

	require.Error(t, deployment.Validate())
}

// A worker validates the same configuration without a state directory, so the
// separation rules cannot refuse a worker that was never given one.
func TestValidateWorkerIgnoresTheStateDirectory(t *testing.T) {
	t.Parallel()

	deployment := stateTestConfig()
	deployment.StateDirectory = ""

	require.NoError(t, deployment.ValidateWorker())
	require.ErrorIs(t, deployment.Validate(), config.ErrInvalidConfig)
}

func isInside(ancestor, candidate string) bool {
	relative, err := filepath.Rel(ancestor, candidate)
	if err != nil {
		return false
	}

	return relative != ".." &&
		!filepath.IsAbs(relative) &&
		relative != "." &&
		!hasParentPrefix(relative)
}

func hasParentPrefix(relative string) bool {
	return len(relative) >= 2 && relative[:2] == ".."
}
