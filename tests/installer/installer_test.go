//go:build installer

package installer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	systemUser      = "itestuser"
	systemUserID    = "2000"
	systemUserHome  = "/home/itestuser"
	systemWrapper   = "/usr/local/bin/peen"
	systemBinary    = "/usr/local/lib/peen/peen"
	systemPeenHome  = "/home/itestuser/.config/peen"
	pathWarning     = "is not on your PATH"
	notRunningYet   = "Peen is not running yet"
	hostRootRefusal = "host root"
	foreignRefusal  = "was not created by this installation"
	maskedValue     = "********"
	restartHint     = "run peen restart"
	outsideRootHint = "peen config set PEEN_WORKSPACE_ROOT "
)

var pidPattern = regexp.MustCompile(`running \(pid (\d+)`)

func TestMain(m *testing.M) {
	setupContext, cancelSetup := context.WithTimeout(context.Background(), suiteSetupTimeout)
	instance, err := setupSuite(setupContext)
	cancelSetup()
	suite = instance
	exitCode := 1
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "set up the installer suite:", err)
	} else {
		exitCode = m.Run()
	}

	teardownContext, cancelTeardown := context.WithTimeout(context.Background(), suiteTeardownTimeout)
	if err := instance.teardown(teardownContext); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "tear down the installer suite:", err)
		exitCode = 1
	}
	cancelTeardown()

	os.Exit(exitCode)
}

func TestInstallerRefusesBadInput(t *testing.T) {
	testCases := []struct {
		name   string
		flags  []string
		latest string
		want   string
	}{
		{name: "unknown flag", flags: []string{"--bogus"}, want: "unknown flag: --bogus"},
		{name: "missing flag value", flags: []string{"--version"}, want: "--version needs a release"},
		{name: "version before config init", flags: []string{"--version", releaseOld}, want: "older than v0.20.0"},
		{name: "latest release before config init", latest: releaseOld, want: "older than v0.20.0"},
		{name: "malformed version", flags: []string{"--version", "main"}, want: "is not a release version"},
		{name: "rolling with a version", flags: []string{"--rolling", "--version", releaseA}, want: "cannot be combined"},
		{name: "user and system", flags: []string{"--user", "--system"}, want: "cannot be used together"},
		{name: "system without root", flags: []string{"--system"}, want: "--system needs root"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.latest != "" {
				t.Cleanup(suite.setLatest(tc.latest))
			}
			instance := newInstall(t, installOptions{})

			outcome := instance.installer(t, tc.flags...)

			assert.NotZero(t, outcome.code)
			assert.Contains(t, outcome.stderr, tc.want)
			assert.NoFileExists(t, instance.wrapper())
			assert.NoDirExists(t, instance.peenHome)
		})
	}
}

func TestInstallerRefusesToReplaceAnUnmanagedCommand(t *testing.T) {
	instance := newInstall(t, installOptions{})
	mine := []byte("#!/bin/sh\necho mine\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(instance.wrapper()), 0o700))
	require.NoError(t, os.WriteFile(instance.wrapper(), mine, 0o700)) //nolint:gosec // The user's own script.

	outcome := instance.installer(t, instance.setupFlags(dockerSpace, controlSpace)...)

	assert.NotZero(t, outcome.code)
	assert.Contains(t, outcome.stderr, "was not installed by Peen")
	content, err := os.ReadFile(instance.wrapper())
	require.NoError(t, err)
	assert.Equal(t, mine, content)
	assert.NoDirExists(t, instance.peenHome)
}

func TestSetupRefusesUnsafeOrIncompleteCombinations(t *testing.T) {
	t.Run("host control space with control agents beside a reachable daemon", func(t *testing.T) {
		instance := newInstall(t, installOptions{})

		outcome := instance.installer(t, instance.setupFlags(hostSpace, controlSpace)...)

		assert.NotZero(t, outcome.code)
		assert.Contains(t, outcome.stderr, hostRootRefusal)
		assert.Contains(t, outcome.stderr, "--agent-space docker")
		assert.NoFileExists(t, instance.envFile())
	})

	t.Run("docker control space with control agents needs a root", func(t *testing.T) {
		instance := newInstall(t, installOptions{})

		outcome := instance.installer(t,
			"--control-space", dockerSpace,
			"--agent-space", controlSpace,
			"--container-name", instance.containerName,
		)

		assert.NotZero(t, outcome.code)
		assert.Contains(t, outcome.stderr, "needs an explicit --workspace-root")
		assert.NoFileExists(t, instance.envFile())
	})

	t.Run("bad setup values", func(t *testing.T) {
		instance := newInstall(t, installOptions{})
		badFlags := map[string][]string{
			"--control-space must be host or docker":  {"--control-space", "vm"},
			"--agent-space must be control or docker": {"--agent-space", "vm"},
			"--listen must look like":                 {"--listen", "everywhere"},
			"does not exist":                          {"--workspace-root", filepath.Join(instance.dir, "missing")},
			"must be an absolute path":                {"--workspace-root", "relative"},
			"--container-name may hold":               {"--container-name", "-bad"},
		}
		for want, flags := range badFlags {
			outcome := instance.installer(t, flags...)

			assert.NotZero(t, outcome.code, flags)
			assert.Contains(t, outcome.stderr, want, flags)
		}
		assert.NoFileExists(t, instance.envFile())
	})
}

func TestPipedUserInstallWarnsAboutPathAndPinsTheRequestedRelease(t *testing.T) {
	instance := newInstall(t, installOptions{pathWithoutBin: true})
	flags := append([]string{"--version", releaseB}, instance.setupFlags(dockerSpace, controlSpace)...)

	outcome := instance.installerPiped(t, flags...)

	require.Zero(t, outcome.code, outcome.output())
	assert.Contains(t, outcome.stderr, pathWarning)
	assert.Contains(t, outcome.stdout, `bash:  echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc`)
	assert.Contains(t, outcome.stdout, `zsh:   echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc`)
	assert.Contains(t, outcome.stdout, notRunningYet)
	assert.Contains(t, outcome.stdout, "peen config edit")
	wrapper := requireManagedWrapper(t, instance.wrapper())
	assert.Contains(t, wrapper, releaseMarkerPrefix+releaseB)
	values := instance.readEnv(t)
	assert.Equal(t, suite.imageReference(releaseB), values["PEEN_IMAGE"])
	assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))

	t.Run("a second install keeps the configuration", func(t *testing.T) {
		instance.mustPeen(t, "config", "set", "ITEST_KEPT", "still-here")

		again := instance.installer(t, flags...)

		require.Zero(t, again.code, again.output())
		assert.Equal(t, "still-here", instance.readEnv(t)["ITEST_KEPT"])
		requireManagedWrapper(t, instance.wrapper())
	})
}

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

func TestOneShotWithoutManagedController(t *testing.T) {
	for _, control := range []string{hostSpace, dockerSpace} {
		t.Run(control, func(t *testing.T) {
			instance := newInstall(t, installOptions{})
			instance.mustInstall(t, control, dockerSpace)
			instance.configure(t)

			outcome := instance.peenIn(t, instance.project, "--prompt", "say something")
			assertOneShotJSONL(t, instance, outcome)
			assertStopped(t, instance, control)
			unknown := instance.peenIn(t, instance.project, "say something")
			assert.Equal(t, 1, unknown.code)
			assert.Contains(t, unknown.stderr, "unknown command: say something")
			assert.Empty(t, unknown.stdout, "a bare word must not run a turn")
			for _, flags := range [][]string{
				{"--control-space="}, {"--control-space", ""},
			} {
				flags = append(flags, "--prompt", "say something")
				refused := instance.peenIn(t, instance.project, flags...)
				assert.Equal(t, 1, refused.code)
				assert.Contains(t, refused.stderr,
					"--control-space must be host or docker")
			}

			instance.mustPeen(t, "config", "set", "PEEN_DEFAULT_MODEL", "missing/model")
			instance.extraEnv = append(instance.extraEnv,
				"PEEN_DEFAULT_MODEL="+providerName+"/"+providerModel,
				"LOG_LEVEL=warn",
				"LOG_FORMAT=json", "LOG_ADD_SOURCE=false",
			)
			text := instance.peenIn(t, instance.outside,
				"--model="+providerName+"/"+providerModel,
				"--control-space", control, "--workspace", instance.project,
				"--output", "text", "--prompt", "say something")
			require.Zero(t, text.code, text.output())
			assert.Equal(t, providerAnswer+"\n", text.stdout)
			assertStopped(t, instance, control)
			instance.extraEnv = append(instance.extraEnv,
				"PEEN_WORKSPACE_ROOT="+instance.project,
			)

			outside := instance.peenIn(t, instance.outside, "--prompt", "say something")
			assert.Equal(t, 1, outside.code)
			assert.Contains(t, outside.stderr,
				"outside the workspace root "+canonical(t, instance.project))
			assert.Contains(t, outside.stderr, outsideRootHint+
				canonical(t, instance.outside)+" && peen restart")

			explicit := instance.peenIn(t, instance.project, "exec",
				"--workspace", instance.outside, "say something")
			assert.Equal(t, 1, explicit.code)
			assert.Contains(t, explicit.stderr, outsideRootHint+
				canonical(t, instance.outside)+" && peen restart")
		})
	}
}

func assertOneShotJSONL(t *testing.T, instance *install, outcome result) {
	t.Helper()

	require.Zero(t, outcome.code, outcome.output())
	var types []string
	sessions := map[string]bool{}
	// JSONL holds one frame per line, so an empty line is a defect.
	for line := range strings.SplitSeq(strings.TrimSuffix(outcome.stdout, "\n"), "\n") {
		require.NotEmpty(t, strings.TrimSpace(line), "JSONL output holds an empty line")

		var frame struct {
			Type     string `json:"type"`
			Metadata struct {
				SessionID string `json:"sessionId"`
			} `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame), line)
		types = append(types, frame.Type)
		if frame.Metadata.SessionID != "" {
			sessions[frame.Metadata.SessionID] = true
		}
	}
	assert.Contains(t, types, "turn.completed")
	assert.Contains(t, types, webSocketCompleted)
	for session := range sessions {
		instance.recordSession(session)
	}
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

func TestConfigCommands(t *testing.T) {
	instance := newInstall(t, installOptions{})
	outcome := instance.installer(t, append(instance.setupFlags(dockerSpace, controlSpace), "--rolling")...)
	require.Zero(t, outcome.code, outcome.output())
	assert.Equal(t, suite.imageReference(rollingTag), instance.readEnv(t)["PEEN_IMAGE"])
	assert.Contains(t, requireManagedWrapper(t, instance.wrapper()), releaseMarkerPrefix+rollingRef)
	instance.configure(t)

	t.Run("get and path", func(t *testing.T) {
		assert.Equal(t, dockerSpace+"\n", instance.mustPeen(t, "config", "get", "PEEN_CONTROL_SPACE").stdout)
		assert.Equal(t, instance.envFile()+"\n", instance.mustPeen(t, "config", "path").stdout)
		missing := instance.peen(t, "config", "get", "ITEST_NEVER_SET")
		assert.NotZero(t, missing.code)
	})

	t.Run("set keeps one line and the value verbatim", func(t *testing.T) {
		value := `a|b&c\d"e'f$g=h {"x":1}`
		instance.mustPeen(t, "config", "set", "ITEST_PLAIN", "first")
		instance.mustPeen(t, "config", "set", "ITEST_PLAIN", value)

		assert.Equal(t, value+"\n", instance.mustPeen(t, "config", "get", "ITEST_PLAIN").stdout)
		assert.Equal(t, value, instance.readEnv(t)["ITEST_PLAIN"])
		content, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(content), "\nITEST_PLAIN="))
		assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))
	})

	t.Run("show masks every secret", func(t *testing.T) {
		instance.mustPeen(t, "config", "set", "ITEST_SECRET_TOKEN", "s3cret-value")

		shown := instance.mustPeen(t, "config", "show").stdout

		for _, secret := range []string{providerKey, apiToken, "s3cret-value"} {
			assert.NotContains(t, shown, secret)
		}
		for _, key := range []string{providerKeyName, "PEEN_API_TOKEN", "ITEST_SECRET_TOKEN"} {
			assert.Contains(t, shown, "\n"+key+"="+maskedValue+"\n")
		}
		assert.Contains(t, shown, "\nPEEN_CONTROL_SPACE="+dockerSpace+"\n")
	})

	t.Run("set refuses bad input and leaves the file alone", func(t *testing.T) {
		before, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		testCases := []struct {
			arguments []string
			want      string
		}{
			{arguments: []string{"1BAD", "x"}, want: "invalid setting name"},
			{arguments: []string{"BAD-KEY", "x"}, want: "invalid setting name"},
			{arguments: []string{"ITEST_LINES", "a\nb"}, want: "must be a single line"},
			{arguments: []string{"ITEST_LINES", "a\rb"}, want: "must be a single line"},
			{arguments: []string{"PEEN_CONTROL_SPACE", hostSpace}, want: "peen setup --control-space"},
			{arguments: []string{"PEEN_IMAGE", "other:tag"}, want: "peen upgrade"},
			{arguments: []string{"ONLY_KEY"}, want: "usage: peen config set"},
		}
		for _, tc := range testCases {
			outcome := instance.peen(t, append([]string{"config", "set"}, tc.arguments...)...)

			assert.NotZero(t, outcome.code, tc.arguments)
			assert.Contains(t, outcome.stderr, tc.want, tc.arguments)
		}
		after, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))
	})
}

func TestDoctor(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, dockerSpace, controlSpace)

	fresh := instance.peen(t, "doctor")
	assert.Equal(t, 1, fresh.code)
	assert.Contains(t, fresh.stdout, "FAIL  provider key")
	assert.Contains(t, fresh.stdout, "FAIL  the controller is not running")
	assert.Contains(t, fresh.stdout, "ok    "+instance.envFile()+" has mode 600")

	instance.configure(t)
	instance.mustPeen(t, "start")
	waitHealthy(t, instance)

	healthy := instance.peen(t, "doctor")
	assert.Zero(t, healthy.code, healthy.stdout)
	assert.Contains(t, healthy.stdout, "all checks passed")
	assert.Contains(t, healthy.stdout, "ok    GET /api/v1/models answers")
	assert.NotContains(t, healthy.stdout, "FAIL")

	instance.mustPeen(t, "stop")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", instance.listen())
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, os.Chmod(instance.envFile(), 0o644)) //nolint:gosec // The test loosens it on purpose.

	broken := instance.peen(t, "doctor")

	assert.Equal(t, 1, broken.code)
	assert.Contains(t, broken.stdout, "is already in use")
	assert.Contains(t, broken.stdout, "has mode 644, want 600")
}

func TestUpgradeMovesTheDockerControlSpaceToTheNewRelease(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, dockerSpace, dockerSpace)
	instance.configure(t)
	instance.mustPeen(t, "start")
	waitHealthy(t, instance)
	imageA := suite.imageReference(releaseA)
	imageB := suite.imageReference(releaseB)
	imageLatest := suite.imageReference(rollingTag)
	t.Cleanup(suite.setLatest(releaseB))

	instance.mustPeen(t, "upgrade")

	assertPinned(t, instance, imageB)
	assert.Contains(t, requireManagedWrapper(t, instance.wrapper()), releaseMarkerPrefix+releaseB)
	waitHealthy(t, instance)
	inspected, exists := inspectContainer(t, instance.containerName)
	require.True(t, exists)
	assert.Equal(t, imageB, inspected.Config.Image)
	assert.False(t, imagePresent(t, imageA), "upgrade must remove the previous image")
	session := openedSession(t, instance, instance.peen(t, "open", instance.project))
	sendTurn(t, instance, session)
	assert.Contains(t, sessionWorkerImages(t, session), imageB)

	t.Run("rolling", func(t *testing.T) {
		instance.mustPeen(t, "upgrade", "--rolling")

		assertPinned(t, instance, imageLatest)
		assert.Contains(t, requireManagedWrapper(t, instance.wrapper()), releaseMarkerPrefix+rollingRef)
		waitHealthy(t, instance)
		rolled, running := inspectContainer(t, instance.containerName)
		require.True(t, running)
		assert.Equal(t, imageLatest, rolled.Config.Image)
		assert.False(t, imagePresent(t, imageB))
	})

	// PEEN_WORKER_IMAGE replaces every profile's image, so an operator
	// clears it to let profile images apply. Upgrade must keep that choice.
	t.Run("a cleared worker image survives an upgrade", func(t *testing.T) {
		instance.mustPeen(t, "config", "set", "PEEN_WORKER_IMAGE", "")

		instance.mustPeen(t, "upgrade")

		values := instance.readEnv(t)
		assert.Equal(t, imageB, values["PEEN_IMAGE"])
		assert.Empty(t, values["PEEN_WORKER_IMAGE"])
		waitHealthy(t, instance)
	})
}

func TestUpgradeMovesTheHostControlSpaceToTheNewRelease(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, hostSpace, dockerSpace)
	instance.configure(t)
	instance.mustPeen(t, "start")
	waitHealthy(t, instance)
	firstPID := controllerIdentity(t, instance, hostSpace)
	imageA := suite.imageReference(releaseA)
	imageB := suite.imageReference(releaseB)
	t.Cleanup(suite.setLatest(releaseB))

	instance.mustPeen(t, "upgrade")

	assertPinned(t, instance, imageB)
	imageFile, err := os.ReadFile(filepath.Join(filepath.Dir(instance.binary()), "image"))
	require.NoError(t, err)
	assert.Equal(t, imageB+"\n", string(imageFile))
	assert.Contains(t, requireManagedWrapper(t, instance.wrapper()), releaseMarkerPrefix+releaseB)
	waitHealthy(t, instance)
	assert.NotEqual(t, firstPID, controllerIdentity(t, instance, hostSpace))
	assert.False(t, imagePresent(t, imageA))
}

func assertPinned(t *testing.T, instance *install, image string) {
	t.Helper()

	values := instance.readEnv(t)
	assert.Equal(t, image, values["PEEN_IMAGE"])
	assert.Equal(t, image, values["PEEN_WORKER_IMAGE"])
}

func TestUninstall(t *testing.T) {
	t.Run("keep data", func(t *testing.T) {
		instance := newInstall(t, installOptions{})
		instance.mustInstall(t, dockerSpace, controlSpace)
		instance.configure(t)
		instance.mustPeen(t, "start")
		waitHealthy(t, instance)

		instance.mustPeen(t, "uninstall", "--keep-data")

		_, exists := inspectContainer(t, instance.containerName)
		assert.False(t, exists)
		assert.NoFileExists(t, instance.wrapper())
		assert.NoDirExists(t, filepath.Dir(instance.binary()))
		assert.FileExists(t, instance.envFile())
		assert.False(t, imagePresent(t, suite.imageReference(releaseA)))
	})

	t.Run("delete data", func(t *testing.T) {
		instance := newInstall(t, installOptions{})
		instance.mustInstall(t, hostSpace, dockerSpace)
		instance.configure(t)
		instance.mustPeen(t, "start")
		waitHealthy(t, instance)

		instance.mustPeen(t, "uninstall", "--delete-data")

		assert.NoFileExists(t, instance.wrapper())
		assert.NoFileExists(t, instance.binary())
		assert.NoDirExists(t, instance.peenHome)
		assert.NotEqual(t, http.StatusOK, healthStatus(t, instance))
		assert.DirExists(t, instance.home)
	})

	t.Run("refuses without an answer", func(t *testing.T) {
		instance := newInstall(t, installOptions{})
		instance.mustInstall(t, dockerSpace, controlSpace)

		outcome := instance.peen(t, "uninstall")

		assert.Equal(t, 1, outcome.code)
		assert.Contains(t, outcome.stderr, "pass --keep-data or --delete-data")
		assert.FileExists(t, instance.wrapper())
		assert.FileExists(t, instance.envFile())
	})
}

func TestForeignContainerIsNeverTouched(t *testing.T) {
	instance := newInstall(t, installOptions{})
	foreign := instance.containerName + "-foreign"
	foreignID, err := docker(t.Context(), "create", "--name", foreign, suite.image)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := docker(context.Background(), "rm", "-f", foreignID); err != nil {
			t.Errorf("remove %s: %v", foreign, err)
		}
	})

	t.Run("first setup refuses the name", func(t *testing.T) {
		other := newInstall(t, installOptions{})
		flags := other.setupFlags(dockerSpace, controlSpace)
		flags[len(flags)-1] = foreign

		outcome := other.installer(t, flags...)

		assert.NotZero(t, outcome.code)
		assert.Contains(t, outcome.stderr, foreignRefusal)
		assert.NoFileExists(t, other.envFile())
	})

	instance.mustInstall(t, dockerSpace, controlSpace)
	instance.configure(t)
	instance.mustPeen(t, "config", "set", "PEEN_CONTAINER_NAME", foreign)

	for _, arguments := range [][]string{{"start"}, {"stop"}, {"setup", "--container-name", foreign}} {
		outcome := instance.peen(t, arguments...)

		assert.NotZero(t, outcome.code, arguments)
		assert.Contains(t, outcome.stderr, foreignRefusal, arguments)
	}
	uninstalled := instance.mustPeen(t, "uninstall", "--keep-data")
	assert.Contains(t, uninstalled.stderr, "left the container "+foreign+" alone")

	inspected, exists := inspectContainer(t, foreign)
	require.True(t, exists, "the foreign container must survive")
	assert.Equal(t, foreignID, inspected.ID)
}

// TestSystemInstall installs as root in a throwaway container for a sudo user.
// The command and the binary must belong to root, the configuration to the
// user, and Peen must run as the user.
func TestSystemInstall(t *testing.T) {
	name := containerNamePrefix + suite.runID + "-system"
	startRunnerContainer(t, name, true, "0:0")
	t.Cleanup(func() {
		if _, err := docker(context.Background(), "rm", "-f", name); err != nil {
			t.Errorf("remove %s: %v", name, err)
		}
	})
	_, err := docker(t.Context(), "exec", name, "useradd", "-m", "-u", systemUserID, "-s", "/bin/bash", systemUser)
	require.NoError(t, err)
	listenPort := freePort(t)
	target := &install{listenPort: listenPort}
	installScript := filepath.Join(suite.repoRoot, "install.sh")
	installEnvironment := []string{
		"PEEN_INSTALL_REGISTRY_URL=" + suite.proxy.URL,
		"PEEN_INSTALL_SOURCE_URL=" + suite.sourceURL,
		"PEEN_INSTALL_LATEST_URL=" + suite.latestURL(),
		"PEEN_NO_BROWSER=1",
	}
	asRoot := func(environment []string, arguments ...string) (string, error) {
		dockerArguments := []string{"exec", "-w", "/"}
		for _, entry := range environment {
			dockerArguments = append(dockerArguments, "-e", entry)
		}

		return docker(t.Context(), append(append(dockerArguments, name), arguments...)...)
	}
	asUser := func(arguments ...string) (string, error) {
		return docker(t.Context(), append([]string{
			"exec", "--user", systemUser, "-w", systemUserHome,
			"-e", "HOME=" + systemUserHome, "-e", "PEEN_NO_BROWSER=1",
			name,
		}, arguments...)...)
	}

	t.Run("refusals", func(t *testing.T) {
		testCases := map[string][]string{
			"SUDO_USER is empty or root": {"bash", installScript, "--system"},
			"do not install for root":    {"bash", installScript, "--user"},
		}
		for want, arguments := range testCases {
			output, err := asRoot(installEnvironment, arguments...)
			require.Error(t, err)
			assert.Contains(t, output, want)
		}
		output, err := asRoot(append(installEnvironment, "SUDO_USER=root"), "bash", installScript, "--system")
		require.Error(t, err)
		assert.Contains(t, output, "SUDO_USER is empty or root")
	})

	output, err := asRoot(
		append(installEnvironment, "SUDO_USER="+systemUser, "HOME=/root"),
		"bash", installScript, "--system",
		"--control-space", hostSpace,
		"--agent-space", controlSpace,
		"--workspace-root", systemUserHome,
		"--listen", "127.0.0.1:"+strconv.Itoa(listenPort),
		"--container-name", name+"-controller",
	)
	require.NoError(t, err, output)
	assert.Contains(t, output, notRunningYet)

	ownership, err := asRoot(nil, "stat", "-c", "%n %U:%G %a",
		systemWrapper, systemBinary,
		systemPeenHome+"/.env", systemPeenHome, systemPeenHome+"/config", systemPeenHome+"/state",
	)
	require.NoError(t, err, ownership)
	assert.Equal(t, strings.Join([]string{
		systemWrapper + " root:root 755",
		systemBinary + " root:root 755",
		systemPeenHome + "/.env " + systemUser + ":" + systemUser + " 600",
		systemPeenHome + " " + systemUser + ":" + systemUser + " 700",
		systemPeenHome + "/config " + systemUser + ":" + systemUser + " 700",
		systemPeenHome + "/state " + systemUser + ":" + systemUser + " 700",
	}, "\n"), ownership)

	upstreams, err := json.Marshal([]map[string]string{{
		"name": providerName, "type": "openai", "baseUrl": suite.provider.url, "apiKeyEnv": providerKeyName,
	}})
	require.NoError(t, err)
	for _, setting := range [][2]string{
		{"PEEN_UPSTREAMS", string(upstreams)},
		{providerKeyName, providerKey},
		{"PEEN_DEFAULT_MODEL", providerName + "/" + providerModel},
		{"PEEN_COMPACTION_MODEL", providerName + "/" + providerModel},
		{"PEEN_API_TOKEN", apiToken},
		{"PEEN_METRICS_LISTEN_ADDRESS", "127.0.0.1:" + strconv.Itoa(freePort(t))},
	} {
		output, err := asUser("peen", "config", "set", setting[0], setting[1])
		require.NoError(t, err, output)
	}
	project := systemUserHome + "/proj"
	_, err = asUser("mkdir", "-p", project)
	require.NoError(t, err)

	output, err = asUser("peen", "open", project)
	require.NoError(t, err, output)
	waitHealthy(t, target)
	match := sessionURLPattern.FindStringSubmatch(output)
	require.NotNil(t, match, output)
	sendTurn(t, target, match[2])
	processOwner, err := asRoot(nil, "bash", "-c", `stat -c %U "/proc/$(cat "$1")"`, "_", systemPeenHome+"/state/controller.pid")
	require.NoError(t, err, processOwner)
	assert.Equal(t, systemUser, strings.TrimSpace(processOwner))

	output, err = asUser("peen", "stop")
	require.NoError(t, err, output)
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

func canonical(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return resolved
}
