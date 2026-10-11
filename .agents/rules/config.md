# Configuration

Peen reads its deployment settings from environment variables once, at startup, in `internal/pkg/config`. A bad value stops startup with a clear error. There is no reload: a change means a restart.

## Adding or changing a setting

- Name it `PEEN_<SETTING>` in `UPPER_SNAKE_CASE`. `LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE` are the exceptions, owned by the logging library.
- Declare it on `config.Config` with `env:"PEEN_X"` and a `default:"..."` tag, or `env:"PEEN_X,required"`. Parse with `github.com/psyb0t/gonfiguration`. Do not read `os.Getenv` for configuration.
- Validate ranges, formats, and cross-field rules in `Validate`, so a mistake fails at startup instead of mid-turn.
- Pick defaults that are safe: loopback listeners, `info` logging, conservative limits.
- Decide whether workers need it. `workerRuntimeEnvironmentKey` in `internal/pkg/config/workerenvironment.go` is the allowlist of what the controller passes to a worker. Add a setting there only when the worker reads it. Controller-only paths and authority never go to a worker.
- Document it in the same change: a commented entry in `.env.example` with an obviously fake value, a row in `docs/configuration.md`, and a CHANGELOG line. Removing or renaming one is a breaking change. See `versioning.md`.

## Secrets in configuration

- Provider keys are never values inside `PEEN_UPSTREAMS`. An upstream names the variable that holds its key with `apiKeyEnv`, and Peen reads that variable.
- A setting whose value is a secret is never logged, never echoed in an error, and never stored in SQLite. See `security.md`.
- `.env` is gitignored and is a Docker `--env-file`. Do not write code or docs that `source` it from a shell, because `PEEN_UPSTREAMS` holds raw JSON.

## Directories

`PEEN_CONFIG_DIR` and `PEEN_STATE_DIR` must stay separate. Startup refuses equal or nested paths after resolving symlinks. Docker workers receive the config directory read-only and never receive the state directory. A `control` worker runs as the controller's user and reaches both. Keep both rules when touching path handling.
