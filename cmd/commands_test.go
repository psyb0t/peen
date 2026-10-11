package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/config/envtemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const peenEnvironmentPrefix = "PEEN_"

// launchInputRecorder stands in for the worker's stdin and records whether the
// command read the launch document from it.
type launchInputRecorder struct {
	hasBeenRead bool
}

func (r *launchInputRecorder) Read([]byte) (int, error) {
	r.hasBeenRead = true

	return 0, io.EOF
}

// The worker must not run unprotected: when it cannot mark itself
// non-dumpable it fails before it reads the launch document, which carries the
// worker credential.
func TestWorkerCommandFailsBeforeReadingTheLaunchWhenItCannotBecomeNonDumpable(
	t *testing.T,
) {
	t.Parallel()

	launchInput := &launchInputRecorder{}

	command := workerCommand(workerCommandDependencies{
		enableNonDumpable: func(context.Context) error {
			return ctxerrors.Wrap(
				commerr.ErrPermissionDenied,
				"refuse the non-dumpable flag",
			)
		},
	})
	command.SetIn(launchInput)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{})

	err := command.ExecuteContext(t.Context())

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.False(t, launchInput.hasBeenRead, "the launch document was read")
}

// An installer runs `config init` before any .env exists, so the command must
// work through the real root command with no PEEN_ variable set at all: it
// must not parse Peen's configuration or start a Servicepack service.
func TestConfigInitWritesTheEnvFileWithoutAnyPeenConfiguration(t *testing.T) {
	clearPeenEnvironment(t)

	directory := t.TempDir()
	envFile := filepath.Join(directory, ".env")

	output, err := executeRootCommand(
		t,
		"config", "init",
		"--env-file", envFile,
		"--config-dir", "/home/someone/.config/peen/config",
		"--state-dir", "/home/someone/.config/peen/state",
		"--workspace-root", "/home/someone",
		"--default-agent-space", "docker:default",
		"--worker-image", "example.invalid/peen:v0.0.0",
		"--http-listen-address", "127.0.0.1:8080",
		"--host-username", "someone",
		"--host-home", "/home/someone",
		"--control-space", "docker",
		"--image", "example.invalid/peen:v0.0.0",
		"--container-name", "peen-someone",
	)
	require.NoError(t, err, output)
	assert.Contains(t, output, envFile)

	info, err := os.Stat(envFile)
	require.NoError(t, err)
	assert.Equal(t, envtemplate.FileMode, info.Mode().Perm())

	written, err := os.ReadFile(envFile)
	require.NoError(t, err)

	lines := strings.Split(string(written), "\n")
	for _, want := range []string{
		"PEEN_CONFIG_DIR=/home/someone/.config/peen/config",
		"PEEN_STATE_DIR=/home/someone/.config/peen/state",
		"PEEN_WORKSPACE_ROOT=/home/someone",
		"PEEN_DEFAULT_AGENT_SPACE=docker:default",
		"PEEN_WORKER_IMAGE=example.invalid/peen:v0.0.0",
		"PEEN_HTTP_LISTEN_ADDRESS=127.0.0.1:8080",
		"PEEN_HOST_USERNAME=someone",
		"PEEN_HOST_HOME=/home/someone",
		"PEEN_CONTROL_SPACE=docker",
		"PEEN_IMAGE=example.invalid/peen:v0.0.0",
		"PEEN_CONTAINER_NAME=peen-someone",
	} {
		assert.Contains(t, lines, want)
	}
}

func TestConfigInitRefusesWithoutWritingAnything(t *testing.T) {
	clearPeenEnvironment(t)

	testCases := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{
			name:    "line break in a value",
			args:    []string{"--image", "a\nPEEN_API_TOKEN=x"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "relative path",
			args:    []string{"--state-dir", "state"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "bad agent space",
			args:    []string{"--default-agent-space", "docker:/x"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "bad control space",
			args:    []string{"--control-space", "control"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "bad container name",
			args:    []string{"--container-name", "_peen"},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			envFile := filepath.Join(directory, ".env")

			args := append(
				[]string{"config", "init", "--env-file", envFile},
				tc.args...,
			)

			output, err := executeRootCommand(t, args...)
			require.ErrorIs(t, err, tc.wantErr, output)

			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestConfigInitLeavesAnExistingEnvFileUnchanged(t *testing.T) {
	clearPeenEnvironment(t)

	envFile := filepath.Join(t.TempDir(), ".env")
	original := []byte("PEEN_API_TOKEN=keep-me\n")
	require.NoError(t, os.WriteFile(envFile, original, envtemplate.FileMode))

	output, err := executeRootCommand(
		t,
		"config", "init",
		"--env-file", envFile,
	)
	require.ErrorIs(t, err, commerr.ErrAlreadyExists, output)
	assert.Contains(t, err.Error(), envFile)

	kept, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Equal(t, original, kept)
}

func TestConfigInitRequiresTheEnvFile(t *testing.T) {
	clearPeenEnvironment(t)

	_, err := executeRootCommand(t, "config", "init")
	require.Error(t, err)
}

// executeRootCommand runs args through the same root command main builds, so a
// startup step the real binary runs before a subcommand runs here too.
func executeRootCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer

	root := buildRootCommand()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(args)

	err := root.ExecuteContext(t.Context())

	return output.String(), err
}

// clearPeenEnvironment unsets every PEEN_ variable for the test and restores
// them afterwards. t.Setenv registers the restore, then the variable is
// removed so a parse sees it missing rather than empty.
func clearPeenEnvironment(t *testing.T) {
	t.Helper()

	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, peenEnvironmentPrefix) {
			continue
		}

		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
}
