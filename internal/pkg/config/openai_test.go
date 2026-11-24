package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAISettingsStayControllerOnly(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"PEEN_OPENAI_API", "PEEN_OPENAI_WORKSPACE"} {
		assert.True(t, workerEnvironmentReserved(key))
		assert.False(t, workerRuntimeEnvironmentKey(key))
		assert.False(t, controlWorkerEnvironmentKey(key, []string{key}))
	}
	cfg := testConfig(t)
	cfg.OpenAIWorkspace = "relative/workspace"
	require.ErrorIs(t, cfg.Validate(), ErrInvalidConfig)
	cfg.OpenAIWorkspace = t.TempDir()
	require.NoError(t, cfg.Validate())
}
