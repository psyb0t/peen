package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileAPIBoundIsControllerOnlyAndValidated(t *testing.T) {
	t.Parallel()
	for _, limit := range []int64{-1, maximumFileAPIBytes + 1} {
		require.ErrorIs(t, (Config{
			MaxFileAPIBytes: limit,
		}).validateFileAPILimit(), ErrInvalidConfig)
	}

	for _, limit := range []int64{0, 1, 16 << 20, maximumFileAPIBytes} {
		require.NoError(t, (Config{
			MaxFileAPIBytes: limit,
		}).validateFileAPILimit())
	}

	const key = "PEEN_MAX_FILE_API_BYTES"
	assert.False(t, workerRuntimeEnvironmentKey(key))
	assert.True(t, workerEnvironmentReserved(key))
	assert.NotContains(t, ControlWorkerEnvironment([]string{key + "=1"}, nil),
		key+"=1")
}
