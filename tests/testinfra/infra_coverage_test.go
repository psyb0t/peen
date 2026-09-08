package testinfra

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppCoverageFor(t *testing.T) {
	root := t.TempDir()
	coverageDirectory := filepath.Join(root, ".cover", "covdata")
	require.NoError(t, os.MkdirAll(coverageDirectory, appFixtureDirectoryMode))

	t.Setenv(coverageDirectoryEnv, coverageDirectory)

	coverage, err := appCoverageFor(root)

	require.NoError(t, err)
	assert.Equal(t, coverageDirectory, coverage.hostDirectory)
}

func TestAppCoverageForRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	coverageDirectory := t.TempDir()
	t.Setenv(coverageDirectoryEnv, coverageDirectory)

	_, err := appCoverageFor(root)

	require.Error(t, err)
}

func TestAppContainerRequestConfiguresCoverage(t *testing.T) {
	coverageDirectory := t.TempDir()
	request := appContainerRequest(
		t.TempDir(),
		map[string]string{},
		appCoverage{hostDirectory: coverageDirectory},
	)

	hostConfig := container.HostConfig{}
	request.HostConfigModifier(&hostConfig)

	assert.Equal(t, appCoverageDirectory, request.Env[appCoverageEnvironment])
	assert.Equal(t, appCoverageEnabled, *request.BuildArgs[appCoverageBuildArgument])
	assert.Equal(
		t,
		[]string{coverageDirectory + ":" + appCoverageDirectory},
		hostConfig.Binds,
	)
	assert.Equal(
		t,
		container.NetworkMode(appHostNetwork),
		hostConfig.NetworkMode,
	)
	assert.Equal(
		t,
		strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()),
		request.User,
	)
}
