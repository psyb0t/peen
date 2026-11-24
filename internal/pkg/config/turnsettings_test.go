package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnSettingsValidationAndWorkerEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config Config
		valid  bool
	}{
		{name: "defaults", valid: true},
		{name: "valid", config: Config{TurnTimeout: time.Minute, OutputSchemaAttempts: 10, MaxOutputSchemaBytes: 1}, valid: true},
		{name: "negative timeout", config: Config{TurnTimeout: -time.Second}},
		{name: "negative attempts", config: Config{OutputSchemaAttempts: -1}},
		{name: "too many attempts", config: Config{OutputSchemaAttempts: 11}},
		{name: "negative bound", config: Config{MaxOutputSchemaBytes: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.config.validateTurnSettings()
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}

	for _, key := range []string{
		"PEEN_TURN_TIMEOUT", "PEEN_OUTPUT_SCHEMA_ATTEMPTS", "PEEN_MAX_OUTPUT_SCHEMA_BYTES",
	} {
		assert.True(t, workerRuntimeEnvironmentKey(key))
	}
}
