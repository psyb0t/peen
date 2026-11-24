# Shell scripts

Scripts live under `scripts/` (Make helpers in `scripts/make/`, web helpers in `scripts/web/`), plus `docker/entrypoint.sh` and the root `install.sh`. Scripts in `scripts/make/servicepack/` belong to Servicepack and are not edited here.

- Diagnostics use a `log LEVEL message` function that writes one JSON object per line to stderr. `scripts/make/lint.sh` and `scripts/web/run.sh` show the pattern. A Peen override of a framework script may use the `info`, `error`, and `section` helpers from `scripts/make/servicepack/common.sh` instead.
- A non-trivial script tees its output to `${LOG_FILE:-/tmp/peen-<name>.log}`. `docker/entrypoint.sh` does not, because the worker reads its launch document from stdin.
- `scripts/peen.sh` is the installed `peen` command. It never sources `.env`, because `PEEN_UPSTREAMS` holds raw JSON, and it starts a host controller with only the environment `host_environment` builds.

## Lint

`make format` runs shfmt and `make lint` runs shfmt and shellcheck over everything under `scripts/`, which includes `scripts/peen.sh`. `make lint` also checks `install.sh`. `docker/entrypoint.sh` sits outside both, so check it with shfmt and shellcheck inside `make shell` after editing. A `# shellcheck disable=SCxxxx` needs a comment with the reason.
