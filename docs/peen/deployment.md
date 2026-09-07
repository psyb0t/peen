# Deployment

Three equivalent ways to run Peen: a source build, `go install`, or the
production Docker image. All three read the same `PEEN_` environment
variables and the same `PEEN_CONFIG_DIR` state, so pick whichever fits your
environment.

## Source build

```bash
make build
```

This runs a pinned `golang:1.26.6-alpine` Docker image to produce a static,
`CGO_ENABLED=0` Linux binary at `./build/peen`. No local Go toolchain is
required. Run it directly:

```bash
set -a && source .env && set +a
./build/peen run
```

If you have a matching local Go toolchain and prefer to skip Docker for the
build step itself:

```bash
CGO_ENABLED=0 go build -ldflags "-X main.appName=peen" -o ./build/peen ./cmd
./build/peen run
```

The `-ldflags` value matters: without it the binary reports its own name as
`servicepack`, the framework's name, in its own `--help` output and process
scope, instead of `peen`.

## go install

```bash
go install -ldflags "-X main.appName=peen" github.com/psyb0t/peen/cmd@latest
```

The module's single main package lives at `cmd/`, so the installed binary is
named `cmd`, not `peen`. Rename it after installing if you want the
conventional name:

```bash
mv "$(go env GOPATH)/bin/cmd" "$(go env GOPATH)/bin/peen"
```

Either name runs the same binary; `run` is still the subcommand that starts
the service. The `-ldflags` value only affects what the binary calls itself
in its own `--help` output; omit it and the process still runs identically
under the name `servicepack`.

## Docker

```bash
make docker-build
```

Builds the multi-stage `Dockerfile` and tags the result `peen`. The build
stage compiles the same static binary as `make build`; the runtime stage is
Ubuntu 24.04, pinned by image digest, with `ca-certificates`, `curl`, `git`,
`jq`, `less`, `ripgrep`, `tini`, `tzdata`, and `unzip` installed. Ubuntu was
chosen deliberately: `run_command` needs a real shell and utilities, so the
runtime image is the agent's toolbox, not just a binary carrier. A
distroless or scratch image would leave the agent with nothing to run
commands with.

The container runs as a fixed non-root user, UID and GID `10001`
(`appuser`), under `tini` as PID 1 so processes `run_command` spawns get
reaped instead of becoming zombies. The default command is `--help`; pass
`run` to actually start the service.

### Durable state and mounts

Peen keeps everything that must survive a restart under `PEEN_CONFIG_DIR`
(the SQLite database, `AGENTS.md` and friends, per-agent-run JSONL mirrors),
and treats `PEEN_WORKING_DIR` as the default message workspace. Mount both
from the host:

```bash
mkdir -p ./data/peen ./workspace
sudo chown 10001:10001 ./data/peen ./workspace

docker run --rm \
  --env-file .env \
  -p 8080:8080 \
  -v "$(pwd)/data/peen:/data/peen" \
  -v "$(pwd)/workspace:/workspace" \
  peen run
```

Set `PEEN_CONFIG_DIR=/data/peen` and `PEEN_WORKING_DIR=/workspace` in `.env`
to match the mount targets above, or change both sides consistently.

Two ownership requirements matter here, and skipping either produces a
startup failure rather than a silent misconfiguration:

- **`PEEN_CONFIG_DIR`'s host directory must be owned by UID/GID `10001`
  before the container starts.** Peen enforces mode `0700` on this directory
  and `0600` on `peen.db` on every startup, and enforcing a mode requires
  owning the path. A plain `docker run -v host-path:container-path` creates a
  missing `host-path` as root, and a container process running as UID
  `10001` cannot then `chmod` a root-owned directory. Create and `chown` it
  on the host first, as above.
- **`PEEN_WORKING_DIR`'s host directory must already exist.** Peen changes
  into it during startup and does not create it; a missing directory is a
  startup error, not an empty workspace.

### Networking and the API token

Publish whatever `PEEN_HTTP_LISTEN_ADDRESS` binds to (`-p 8080:8080` for the
default `:8080`). If you expose the container beyond a trusted network, set
`PEEN_API_TOKEN` to a real value; it is empty, and therefore unauthenticated,
by default. See [Bearer authentication](../../README.md#bearer-authentication).

### Isolation is the operator's job, not the image's

The Ubuntu base and the fixed non-root user are the only isolation Peen
ships with by default. `run_command` and the file tools still have whatever
access UID `10001` has inside the container: everything under the mounted
`PEEN_CONFIG_DIR` and `PEEN_WORKING_DIR`, plus network access unless you
restrict it. Read the root README's [Security](../../README.md#security)
section, and add `--network`, `--cap-drop`, `--read-only` (with explicit
writable mounts for the two directories above), or a seccomp/AppArmor
profile as your deployment needs.
