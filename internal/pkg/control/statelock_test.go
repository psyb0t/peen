package control_test

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateDirectoryLockExcludesAnotherControllerUntilRelease(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	release, err := control.LockStateDirectory(directory)
	require.NoError(t, err)
	secondRelease, err := control.LockStateDirectory(directory)
	require.ErrorIs(t, err, commerr.ErrLockHeld)
	assert.Nil(t, secondRelease)
	release(t.Context())
	relockedRelease, err := control.LockStateDirectory(directory)
	require.NoError(t, err)
	relockedRelease(t.Context())
}

func TestStateDirectoryLocksAreIndependent(t *testing.T) {
	t.Parallel()
	first, err := control.LockStateDirectory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { first(t.Context()) })
	second, err := control.LockStateDirectory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { second(t.Context()) })
}
