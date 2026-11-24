package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecTurnSettingsLoadsSchemaAndRoundsTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"object"}`), 0o600))
	message, err := execMessage("answer", execOptions{
		outputSchema: path, timeout: time.Second + time.Nanosecond,
	})
	require.NoError(t, err)
	require.NotNil(t, message.TimeoutSeconds)
	assert.EqualValues(t, 2, *message.TimeoutSeconds)
	assert.Equal(t, "object", message.OutputSchema["type"])
}

func TestExecTurnSettingsRefusesMalformedInput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(path, []byte(`null`), 0o600))
	_, err := execMessage("answer", execOptions{outputSchema: path})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = execMessage("answer", execOptions{timeout: -time.Second})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
