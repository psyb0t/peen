//go:build installer

package installer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
