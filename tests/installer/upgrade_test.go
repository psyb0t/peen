//go:build installer

package installer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
