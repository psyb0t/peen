//go:build installer

package installer_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
