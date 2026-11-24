package config

import "github.com/psyb0t/ctxerrors"

// DefaultMaxFileAPIBytes bounds a controller without an explicit file limit.
const DefaultMaxFileAPIBytes int64 = 16 << 20

// Keep base64 bodies bounded and their encoded lengths safe on every host.
const maximumFileAPIBytes int64 = 1 << 30

func (c Config) validateFileAPILimit() error {
	if c.MaxFileAPIBytes < 0 || c.MaxFileAPIBytes > maximumFileAPIBytes {
		return ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_MAX_FILE_API_BYTES must be between 1 and 1073741824")
	}

	return nil
}
