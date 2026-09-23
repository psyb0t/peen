package testinfra

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAppImage = "peen-test-image:fixture"

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
	containerRequest := appContainerRequest(
		testAppImage,
		map[string]string{},
		appCoverage{hostDirectory: coverageDirectory},
	)
	buildRequest := appImageBuildRequest(
		t.TempDir(),
		appCoverage{hostDirectory: coverageDirectory},
	)

	hostConfig := container.HostConfig{}
	containerRequest.HostConfigModifier(&hostConfig)

	assert.Equal(
		t,
		appCoverageDirectory,
		containerRequest.Env[appCoverageEnvironment],
	)
	assert.Equal(
		t,
		appCoverageEnabled,
		*buildRequest.BuildArgs[appCoverageBuildArgument],
	)
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
		containerRequest.User,
	)
}

func TestAppContainerRequestDoesNotUseLogReadiness(t *testing.T) {
	request := appContainerRequest(
		testAppImage,
		map[string]string{},
		appCoverage{},
	)

	assert.Nil(t, request.WaitingFor)
}

func TestAppImageBuildOptionsUseBuildKit(t *testing.T) {
	request := appImageBuildRequest(
		t.TempDir(),
		appCoverage{},
	)

	options := appImageBuildOptions(&request, testAppImage)

	assert.Equal(t, build.BuilderBuildKit, options.Version)
	assert.Equal(t, []string{testAppImage}, options.Tags)
	assert.True(t, options.Remove)
	assert.True(t, options.ForceRemove)
}
