//go:build installer

package installer_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerControlSpaceWithDockerAgents(t *testing.T) {
	instance := newInstall(t, installOptions{})

	runCombination(t, instance, dockerSpace, dockerSpace, suite.imageReference(releaseA))
}

func TestDockerControlSpaceWithControlAgents(t *testing.T) {
	instance := newInstall(t, installOptions{})

	runCombination(t, instance, dockerSpace, controlSpace, suite.imageReference(releaseA))

	t.Run("open refuses a directory outside the root", func(t *testing.T) {
		outcome := instance.peen(t, "open", instance.outside)

		assert.Equal(t, 1, outcome.code)
		assert.Contains(t, outcome.stderr, outsideRootHint+canonical(t, instance.outside)+" && peen restart")
	})

	t.Run("config changes while running", func(t *testing.T) {
		instance.mustPeen(t, "start")
		waitHealthy(t, instance)

		outcome := instance.mustPeen(t, "config", "set", "ITEST_WHILE_RUNNING", "1")
		assert.Contains(t, outcome.stdout, restartHint)

		rename := instance.peen(t, "config", "set", "PEEN_CONTAINER_NAME", instance.containerName+"-renamed")
		assert.NotZero(t, rename.code)
		assert.Contains(t, rename.stderr, "run peen stop")

		refused := instance.peen(t, "run", instance.project)
		assert.NotZero(t, refused.code)
		assert.Contains(t, refused.stderr, "run peen stop first")
		instance.mustPeen(t, "stop")
	})

	t.Run("run serves a foreground controller and stops on SIGTERM", func(t *testing.T) {
		runForeground(t, instance)
		_, exists := inspectContainer(t, instance.containerName+"-run")
		assert.False(t, exists, "the foreground container must be removed")
	})
}

func TestHostControlSpaceWithDockerAgents(t *testing.T) {
	instance := newInstall(t, installOptions{})
	tokensBefore := suite.curlTokenRequests.Load()

	runCombination(t, instance, hostSpace, dockerSpace, suite.imageReference(releaseA))

	assert.Greater(t, suite.curlTokenRequests.Load(), tokensBefore, "the binary download must take the Bearer token path")
	imageFile, err := os.ReadFile(filepath.Join(filepath.Dir(instance.binary()), "image"))
	require.NoError(t, err)
	assert.Equal(t, suite.imageReference(releaseA)+"\n", string(imageFile))

	t.Run("run serves a foreground controller and stops on SIGTERM", func(t *testing.T) {
		runForeground(t, instance)
	})
}

// TestHostControlSpaceWithControlAgents runs in a container without the Docker
// socket, because this test process can reach Docker and the wrapper rightly
// refuses host+control beside it. It installs the release published as a
// multi-platform index, so the binary download must pick this architecture.
func TestHostControlSpaceWithControlAgents(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.runner = startRunnerContainer(t, instance.containerName+"-runner", false, suite.uid+":"+suite.gid)

	runCombination(t, instance, hostSpace, controlSpace, suite.imageReference(releaseIndex), "--version", releaseIndex)
}

func runCombination(t *testing.T, instance *install, control, agent, image string, extraFlags ...string) {
	t.Helper()

	expectedSpace := controlSpace
	if agent == dockerSpace {
		expectedSpace = dockerAgent
	}

	outcome := instance.installer(t, append(instance.setupFlags(control, agent), extraFlags...)...)
	require.Zero(t, outcome.code, outcome.output())
	assert.Contains(t, outcome.stdout, notRunningYet)
	assert.NotContains(t, outcome.stderr, pathWarning)
	requireManagedWrapper(t, instance.wrapper())
	assertLayout(t, instance, control, expectedSpace, image)
	if control == hostSpace {
		assert.Equal(t, os.FileMode(0o755), fileMode(t, instance.binary()))
	}
	instance.configure(t)

	stopped := instance.peen(t, "status")
	assert.Equal(t, statusStopped, stopped.code)
	assert.Contains(t, stopped.stdout, "controller:     stopped")

	instance.mustPeen(t, "start")
	waitHealthy(t, instance)
	firstIdentity := controllerIdentity(t, instance, control)
	if control == dockerSpace {
		assertControllerContainer(t, instance, agent == dockerSpace, image)
	}
	assert.Equal(t, expectedSpace, defaultAgentSpace(t, instance))

	session := openedSession(t, instance, instance.peen(t, "open", instance.project))
	for _, arguments := range [][]string{{}, {"."}} {
		again := openedSession(t, instance, instance.peenIn(t, instance.project, arguments...))
		assert.Equal(t, session, again, "peen %v", arguments)
	}
	relative := openedSession(t, instance, instance.peenIn(t, instance.root, "./proj"))
	assert.Equal(t, session, relative)
	space, apiSession := sessionAgentSpace(t, instance, canonical(t, instance.project))
	assert.Equal(t, expectedSpace, space)
	assert.Equal(t, session, apiSession)

	sendTurn(t, instance, session)
	oneShot := instance.peenIn(t, instance.project, "--prompt", "say something")
	assertOneShotJSONL(t, instance, oneShot)
	text := instance.peenIn(t, instance.project,
		"--output", "text", "--prompt", "say something")
	require.Zero(t, text.code, text.output())
	assert.Equal(t, providerAnswer+"\n", text.stdout)
	if agent == dockerSpace {
		images := sessionWorkerImages(t, session)
		require.NotEmpty(t, images, "a docker agent space must launch a worker container")
		for _, workerImage := range images {
			assert.Equal(t, image, workerImage)
		}
	}

	// The server binary's client commands print through cobra, which writes
	// to stderr, so the forwarded output is checked on both streams.
	listed := instance.mustPeen(t, "session", "list")
	assert.Contains(t, listed.output(), session, "peen session list must reach the server binary's own command")
	instance.mustPeen(t, "control", "status")

	running := instance.mustPeen(t, "status")
	assert.Contains(t, running.stdout, "health:         ok")
	assert.Contains(t, running.stdout, "image:          "+image)
	logs := instance.mustPeen(t, "logs", "20")
	assert.NotEmpty(t, strings.TrimSpace(logs.stdout))

	instance.mustPeen(t, "restart")
	waitHealthy(t, instance)
	assert.NotEqual(t, firstIdentity, controllerIdentity(t, instance, control), "restart must replace the controller")

	instance.mustPeen(t, "stop")
	assertStopped(t, instance, control)
	assert.Equal(t, statusStopped, instance.peen(t, "status").code)
	assert.Contains(t, instance.mustPeen(t, "control", "status").output(), "controller: not running")
	assertStopped(t, instance, control)
}

func assertLayout(t *testing.T, instance *install, control, agentSpace, image string) {
	t.Helper()

	assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))
	for _, directory := range []string{instance.peenHome, filepath.Join(instance.peenHome, "config"), filepath.Join(instance.peenHome, "state")} {
		assert.Equal(t, os.FileMode(0o700), fileMode(t, directory), directory)
	}
	values := instance.readEnv(t)
	expected := map[string]string{
		"PEEN_CONTROL_SPACE":       control,
		"PEEN_IMAGE":               image,
		"PEEN_WORKER_IMAGE":        image,
		"PEEN_CONTAINER_NAME":      instance.containerName,
		"PEEN_DEFAULT_AGENT_SPACE": agentSpace,
		"PEEN_WORKSPACE_ROOT":      canonical(t, instance.root),
		"PEEN_CONFIG_DIR":          filepath.Join(instance.peenHome, "config"),
		"PEEN_STATE_DIR":           filepath.Join(instance.peenHome, "state"),
		"PEEN_HTTP_LISTEN_ADDRESS": instance.listen(),
		"PEEN_HOST_HOME":           instance.home,
	}
	for key, want := range expected {
		assert.Equal(t, want, values[key], key)
	}
	assert.NotEmpty(t, values["PEEN_HOST_USERNAME"])
	if instance.runner.container == "" {
		assert.Equal(t, testUser, values["PEEN_HOST_USERNAME"])
	}
}

func assertControllerContainer(t *testing.T, instance *install, withSocket bool, image string) {
	t.Helper()

	inspected, exists := inspectContainer(t, instance.containerName)
	require.True(t, exists)
	assert.Equal(t, suite.uid+":"+suite.gid, inspected.Config.User)
	assert.Equal(t, image, inspected.Config.Image)
	assert.Equal(t, instance.peenHome, inspected.Config.Labels[installLabel])
	assert.False(t, inspected.HostConfig.Privileged)
	assert.Equal(t, "unless-stopped", inspected.HostConfig.RestartPolicy.Name)
	bindings := inspected.HostConfig.PortBindings["8080/tcp"]
	require.Len(t, bindings, 1)
	assert.Equal(t, "127.0.0.1", bindings[0].HostIP)
	assert.Equal(t, strconv.Itoa(instance.listenPort), bindings[0].HostPort)

	root := canonical(t, instance.root)
	for _, path := range []string{filepath.Join(instance.peenHome, "config"), filepath.Join(instance.peenHome, "state"), root} {
		assert.Contains(t, inspected.HostConfig.Binds, path+":"+path)
	}
	socketBind := dockerSocketPath + ":" + dockerSocketPath
	if withSocket {
		assert.Contains(t, inspected.HostConfig.Binds, socketBind)
		assert.Equal(t, []string{suite.socketGroup}, inspected.HostConfig.GroupAdd)

		return
	}
	assert.NotContains(t, inspected.HostConfig.Binds, socketBind)
	assert.Empty(t, inspected.HostConfig.GroupAdd)
}

func controllerIdentity(t *testing.T, instance *install, control string) string {
	t.Helper()

	if control == dockerSpace {
		inspected, exists := inspectContainer(t, instance.containerName)
		require.True(t, exists)

		return inspected.ID
	}
	status := instance.mustPeen(t, "status")
	match := pidPattern.FindStringSubmatch(status.stdout)
	require.NotNil(t, match, status.stdout)

	return match[1]
}

func assertStopped(t *testing.T, instance *install, control string) {
	t.Helper()

	if control == dockerSpace {
		_, exists := inspectContainer(t, instance.containerName)
		assert.False(t, exists, "stop must remove the controller container")

		return
	}
	pidFile := filepath.Join(instance.peenHome, "state", "controller.pid")
	assert.NoFileExists(t, pidFile)
	assert.NotEqual(t, http.StatusOK, healthStatus(t, instance))
}

// runForeground runs `peen run <project>`, checks that it serves with the
// project as its only root, and stops it with SIGTERM.
func runForeground(t *testing.T, instance *install) {
	t.Helper()

	command, output := instance.startForeground(t, instance.wrapper(), "run", instance.project)
	waitHealthy(t, instance)
	status, body := apiCall(t, instance, http.MethodGet, "/api/v1/workspace-roots", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var roots struct {
		Roots []string `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(body, &roots))
	assert.Equal(t, []string{canonical(t, instance.project)}, roots.Roots)

	require.NoError(t, command.Process.Signal(syscall.SIGTERM))
	assert.Zero(t, waitExit(t, command), output.String())
	assert.NotEqual(t, http.StatusOK, healthStatus(t, instance))
}

// The host control space starts the controller with a few login variables and
// .env, nothing else from the shell, so a secret exported there never reaches
// a control agent. A stand-in binary records the environment it was started
// with and exits, which also proves .env reaches it unparsed by the shell.
func TestHostControllerGetsOnlyLoginVariablesAndTheEnvFile(t *testing.T) {
	const (
		shellOnlyKey    = "PEEN_TEST_SHELL_ONLY"
		shellOnlyEntry  = shellOnlyKey + "=must-not-reach-the-controller"
		environmentFile = "environment.out"
		recordingBinary = "#!/bin/sh\nenv > \"$(dirname \"$0\")/" +
			environmentFile + "\"\n" +
			"printf 'error: socket validation at 0.000401403\\n' >&2\nexit 1\n"
		executableMode = 0o755
	)

	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, hostSpace, dockerSpace)
	instance.configure(t)
	require.NoError(t, os.WriteFile(
		instance.binary(),
		[]byte(recordingBinary),
		executableMode,
	))
	instance.extraEnv = append(instance.extraEnv, shellOnlyEntry)

	outcome := instance.peen(t, "start")
	require.NotZero(t, outcome.code, "the stand-in serves nothing, so start must fail:\n%s", outcome.output())
	assert.Contains(t, outcome.stderr, causeHint)
	assert.NotContains(t, outcome.stderr, providerHint)

	recorded, err := os.ReadFile(filepath.Join(filepath.Dir(instance.binary()), environmentFile))
	require.NoError(t, err, "the stand-in binary did not run")
	environment := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(recorded)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		environment[key] = value
	}

	for _, key := range []string{
		shellOnlyKey,
		"PEEN_HOME",
		"PEEN_INSTALL_REGISTRY_URL",
		"PEEN_NO_BROWSER",
	} {
		assert.NotContains(t, environment, key, "a shell variable reached the controller")
	}
	assert.Equal(t, instance.home, environment["HOME"])
	assert.Equal(t, testUser, environment["USER"])
	assert.Contains(t, environment, "PATH")
	envFile := instance.readEnv(t)
	for _, key := range []string{
		"PEEN_UPSTREAMS",
		"PEEN_STATE_DIR",
		"PEEN_API_TOKEN",
		providerKeyName,
	} {
		assert.Equal(t, envFile[key], environment[key], "%s must reach the controller exactly as .env holds it", key)
	}
}
