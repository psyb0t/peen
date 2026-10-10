# Shell scripts

Scripts live under `scripts/` (Make helpers in `scripts/make/`, web helpers in `scripts/web/`), plus `docker/entrypoint.sh` and the root `install.sh`. Scripts in `scripts/make/servicepack/` belong to Servicepack and are not edited here.

## Header and safety

- Start with `#!/bin/bash`, a comment saying what the script does when that is not obvious from its name, and `set -euo pipefail`.
- Add an `ERR` trap that logs the failing line and exit code, so a failure says where it happened.
- Validate arguments and required variables at the top and exit with a clear message. `[[ -n "${WEB_PKG:-}" ]] || { log ERROR "WEB_PKG is required"; exit 1; }`
- Check critical commands explicitly with `if !` or `|| { log ERROR "..."; exit 1; }`. Do not lean on `set -e` alone inside pipes, subshells, or conditions.
- Never write `|| true` or `2>/dev/null` without a comment that says why ignoring the failure is safe.
- Quote every expansion. Use `[[ ]]`, `$(...)`, and `local` variables in functions. Name constants with `readonly`.
- Clean up temporary files and directories with an `EXIT` trap.

## Logging

- Diagnostics go through a `log LEVEL message` function that writes one JSON object per line to stderr with `time`, `level`, `file`, `line`, `func`, and `msg`. Build it with `jq -nc --arg` or escape values first. `scripts/make/lint.sh` and `scripts/web/run.sh` show the pattern. A Peen override of a framework script may use the `info`, `error`, and `section` helpers from `scripts/make/servicepack/common.sh` instead.
- Use levels `DEBUG`, `INFO`, `WARN`, and `ERROR`, with `DEBUG` gated on `${DEBUG:-}`.
- A non-trivial script also tees its output to a log file: `exec > >(tee -a "${LOG_FILE:-/tmp/peen-<name>.log}") 2>&1`. Skip this only when it would break the script's own I/O, as in `docker/entrypoint.sh`, and say why in a comment.
- stdout carries the script's real output. Plain `echo` is for that output only.
- Never print a token, key, or credential. Do not use `set -x` around commands that carry one.

## Lint

`make format` runs shfmt and `make lint` runs shfmt and shellcheck over everything under `scripts/`. `docker/entrypoint.sh` and `install.sh` sit outside that glob, so check them with shfmt and shellcheck inside `make shell` after editing. Fix every finding. A `# shellcheck disable=SCxxxx` needs a comment with the reason.
