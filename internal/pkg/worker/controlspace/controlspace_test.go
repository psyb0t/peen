package controlspace

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// testShell runs the fake worker. The launcher starts
	// `<binary> worker` in the workspace, so with a shell as the binary the
	// shell runs the script named `worker` that the test writes there.
	testShell            = "/bin/sh"
	testWorkerScript     = "env > worker.env\n"
	testWorkerEnvFile    = "worker.env"
	testScriptMode       = 0o600
	testAgentSpaceName   = "control"
	testCredential       = "control-launcher-test-credential"
	testSocketPath       = "/run/peen-test/worker.sock"
	testInheritedConfig  = "/inherited/config"
	testProviderKeyName  = "CONTROL_TEST_PROVIDER_KEY"
	testProviderKeyValue = "control-test-provider-key"
	// testPeenProviderKeyName is a provider credential whose name starts with
	// PEEN_, which an operator may choose with apiKeyEnv.
	testPeenProviderKeyName = "PEEN_CONTROL_TEST_PROVIDER_KEY"
	testOrdinaryName        = "CONTROL_TEST_ORDINARY"
	testOrdinaryValue       = "ordinary-value"
	testAllowedName         = "PEEN_DEFAULT_MODEL"
	testAllowedValue        = "provider/model"
)

// A control worker inherits the controller's environment. It must not get the
// controller's API token, its state directory, its Docker authority, or any
// PEEN_ setting a worker does not read. It keeps the provider credentials its
// model calls need and the ordinary variables the agent's commands need.
func TestLaunchWithholdsControllerSettingsFromTheWorker(t *testing.T) {
	withheld := map[string]string{
		"PEEN_API_TOKEN":           "control-test-api-token",
		"PEEN_STATE_DIR":           "/controller/state",
		"PEEN_DOCKER_SOCKET":       "/controller/docker.sock",
		"DOCKER_HOST":              "unix:///controller/docker.sock",
		"PEEN_HTTP_LISTEN_ADDRESS": "127.0.0.1:1",
		"PEEN_NOT_A_WORKER_KEY":    "controller-only",
	}
	kept := map[string]string{
		testProviderKeyName:     testProviderKeyValue,
		testPeenProviderKeyName: testProviderKeyValue,
		testOrdinaryName:        testOrdinaryValue,
		testAllowedName:         testAllowedValue,
	}

	for key, value := range withheld {
		t.Setenv(key, value)
	}

	for key, value := range kept {
		t.Setenv(key, value)
	}

	t.Setenv("PEEN_CONFIG_DIR", testInheritedConfig)

	workspace := t.TempDir()
	configDirectory := t.TempDir()

	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, worker.WorkerCommand),
		[]byte(testWorkerScript),
		testScriptMode,
	))

	launcher, err := New(Options{
		BinaryPath: testShell,
		ProviderCredentialKeys: []string{
			testProviderKeyName,
			testPeenProviderKeyName,
		},
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document: worker.LaunchDocument{
			SessionID:       uuid.New(),
			GenerationID:    uuid.New(),
			Credential:      testCredential,
			SocketPath:      testSocketPath,
			Workspace:       workspace,
			ConfigDirectory: configDirectory,
			AgentSpace:      testAgentSpaceName,
		},
		AgentSpace: worker.AgentSpace{
			Name: testAgentSpaceName,
			Kind: worker.KindControl,
		},
	})
	require.NoError(t, err)

	exitCode, err := process.Wait()
	require.NoError(t, err)
	require.Zero(t, exitCode)

	environment := readWorkerEnvironment(
		t,
		filepath.Join(workspace, testWorkerEnvFile),
	)

	for key := range withheld {
		assert.NotContains(t, environment, key)
	}

	for key, value := range kept {
		assert.Equal(t, value, environment[key], key)
	}

	assert.NotEmpty(t, environment["PATH"])
	assert.Equal(t, configDirectory, environment["PEEN_CONFIG_DIR"])
}

func readWorkerEnvironment(t *testing.T, path string) map[string]string {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, file.Close()) })

	environment := make(map[string]string)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found {
			environment[key] = value
		}
	}

	require.NoError(t, scanner.Err())

	return environment
}
