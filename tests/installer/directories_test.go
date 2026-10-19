//go:build installer

package installer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	socketLimitFailure = "over the 107 byte limit"
	providerHint       = "check the model provider and its key"
	causeHint          = "the error above names the cause"
)

// Some systems create home directories with the setgid bit, and a directory
// made inside one inherits it. GNU chmod with a four-digit mode keeps that
// bit, so setup must clear it or doctor fails Peen's own fresh install.
func TestSetupClearsAnInheritedSetgidBit(t *testing.T) {
	instance := newInstall(t, installOptions{})
	require.NoError(t, os.Chmod(instance.dir, 0o700|os.ModeSetgid))

	instance.mustInstall(t, dockerSpace, dockerSpace)

	for _, directory := range []string{
		instance.peenHome,
		filepath.Join(instance.peenHome, "config"),
		filepath.Join(instance.peenHome, "state"),
	} {
		info, err := os.Stat(directory)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), directory)
		assert.Zero(t, info.Mode()&os.ModeSetgid, directory)
	}

	doctor := instance.peen(t, "doctor")
	assert.Contains(t, doctor.stdout, "ok    "+instance.peenHome+" has mode 700")
	assert.NotContains(t, doctor.stdout, "has mode 2700")
}

// A worker socket root too deep for a Unix socket used to pass doctor, then
// fail at start with a hint that blamed the model provider.
func TestDoctorAndStartExplainATooLongWorkerSocketRoot(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, dockerSpace, dockerSpace)
	instance.configure(t)

	deep := filepath.Join(instance.dir, strings.Repeat("s", 70))
	instance.mustPeen(t, "config", "set", "PEEN_WORKER_SOCKET_DIR", deep)

	doctor := instance.peen(t, "doctor")
	assert.Equal(t, 1, doctor.code)
	assert.Contains(t, doctor.stdout, "FAIL  worker sockets under "+deep)
	assert.Contains(t, doctor.stdout, socketLimitFailure)
	assert.Contains(t, doctor.stdout, "PEEN_WORKER_SOCKET_DIR")

	started := instance.peen(t, "start")
	assert.NotZero(t, started.code)
	assert.Contains(t, started.stderr, "PEEN_WORKER_SOCKET_DIR")
	assert.Contains(t, started.stderr, causeHint)
	assert.NotContains(t, started.stderr, providerHint)
}

// The Docker control space mounts only the directories it knows. A worker
// socket directory moved out of the state directory must be mounted too, or
// the controller binds sockets inside its own container where no worker can
// reach them.
func TestDockerControlSpaceMountsAMovedWorkerSocketDirectory(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, dockerSpace, dockerSpace)
	instance.configure(t)

	sockets := filepath.Join(instance.dir, "s")
	instance.mustPeen(t, "config", "set", "PEEN_WORKER_SOCKET_DIR", sockets)

	doctor := instance.peen(t, "doctor")
	assert.Contains(t, doctor.stdout, "ok    worker sockets under "+sockets)

	instance.mustPeen(t, "start")
	waitHealthy(t, instance)

	assert.Equal(t, os.FileMode(0o700), fileMode(t, sockets))

	session := openedSession(t, instance, instance.peen(t, "open",
		instance.project))
	sendTurn(t, instance, session)
	assert.NotEmpty(t, sessionWorkerImages(t, session),
		"the turn must run in a Docker worker reached through the moved "+
			"socket directory")

	instance.mustPeen(t, "stop")
}
