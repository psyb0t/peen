package config

import "github.com/psyb0t/ctxerrors"

// MaxOutputSchemaAttempts bounds final-answer retries for one turn.
const MaxOutputSchemaAttempts = 10

func (c Config) validateTurnSettings() error {
	if c.TurnTimeout < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_TURN_TIMEOUT must be non-negative")
	}

	if c.OutputSchemaAttempts < 0 ||
		c.OutputSchemaAttempts > MaxOutputSchemaAttempts {
		return ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_OUTPUT_SCHEMA_ATTEMPTS must be from 1 to 10")
	}

	if c.MaxOutputSchemaBytes < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_MAX_OUTPUT_SCHEMA_BYTES must be positive")
	}

	return nil
}
