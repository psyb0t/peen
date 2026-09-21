# Deployment

Run the Docker image unless you have a reason to own the process yourself.
It gives the agent a real shell and command-line tools while keeping its access
inside the mounts you choose. Source builds and `go install` run the same
server, but you then own the operating-system boundary around it.

All launch methods use the same `PEEN_` configuration, trusted
`PEEN_CONFIG_DIR` harness layer, and durable `PEEN_STATE_DIR` state.

## Install script

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

`install.sh` clones the repository into a temporary directory, runs `make
build`, installs the binary through `make install`, and removes the clone on
the way out whether or not the build succeeded. `PREFIX` picks the destination
and `REF` picks a tag or branch:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh |
  PREFIX=/usr/local/bin REF=v0.10.1 bash
```

It needs `git` and `docker` and refuses to start without them. Doing the same
thing by hand is two commands:

```bash
git clone https://github.com/psyb0t/peen.git
cd peen
make install
```

## Install a built binary

```bash
make install
```

This builds first, then copies `./build/peen` to `PREFIX`, which defaults to
`~/bin`. Pass `PREFIX` for anywhere else, with `sudo` when the destination
needs it:

```bash
sudo make install PREFIX=/usr/local/bin
```

The target warns when `PREFIX` is not on your `PATH`.

## Build from source

```bash
make build
```

This runs a pinned `golang:1.26.6-alpine` Docker image to produce a static,
`CGO_ENABLED=0` Linux binary at `./build/peen`. No local Go toolchain is
required.

Do not source `.env` before a bare run. It is Docker `--env-file` syntax and
the raw JSON in `PEEN_UPSTREAMS` is not Bash syntax. Set the values through
your shell or process manager instead:

```bash
root="$PWD"
mkdir -p "$root/data/peen/config" "$root/data/peen/state" "$root/workspace"
export PEEN_CONFIG_DIR="$root/data/peen/config"
export PEEN_STATE_DIR="$root/data/peen/state"
export PEEN_UPSTREAMS='[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]'
export PEEN_DEFAULT_MODEL="aigate/your-model-id"
export PEEN_COMPACTION_MODEL="aigate/your-model-id"
export AIGATE_TOKEN="your-token-here"

cd "$root/workspace"
"$root/build/peen" run
```

If you have a matching local Go toolchain and prefer to skip Docker for the
build step itself:

```bash
CGO_ENABLED=0 go build -o ./build/peen ./cmd
./build/peen run
```

However you build it, the program calls itself `peen` in its own `--help` and
in the `binary` field of every log line. No build flags are needed for that.

## go install

```bash
go install github.com/psyb0t/peen/cmd@latest
```

The module's single main package lives at `cmd/`, so the installed file is
named `cmd`, not `peen`. Rename it if you want the conventional name:

```bash
mv "$(go env GOPATH)/bin/cmd" "$(go env GOPATH)/bin/peen"
```

Either filename runs the same program, `run` is still the subcommand that
starts the service, and it still reports itself as `peen`.

## Docker

```bash
make docker-build
```

Builds the multi-stage `Dockerfile` and tags the result `peen`. The build
stage compiles the same static binary as `make build`; the runtime stage is
Ubuntu 24.04, pinned by image digest, with `ca-certificates`, `curl`, `git`,
`jq`, `less`, `ripgrep`, `sudo`, `tini`, `tzdata`, `unzip`, and `util-linux`
installed. `sudo` authorizes nobody by default: the image ships no sudoers rule,
and only a worker on a profile with `allowPrivilegeEscalation` writes one for
its own account. `util-linux` supplies the `setpriv` the entrypoint drops
privileges with. Ubuntu was
chosen deliberately: `run_command` needs a real shell and utilities, so the
runtime image is the agent's toolbox, not just a binary carrier. A
distroless or scratch image would leave the agent with nothing to run
commands with.

The image defaults to a fixed non-root user, UID and GID `10001` (`appuser`),
under `tini` as PID 1 so processes `run_command` spawns get reaped instead of
becoming zombies. The default command is `run`, so `docker run peen` starts a
control plane. When that controller will work with host bind mounts or launch
Docker workers, run it as your host UID and GID as shown below.

The same image is also the worker image. A controller on a Docker execution
profile creates `peen-worker-<session-uuid>` from it with the `worker` command
and hands that worker its launch document on stdin. That path needs the
controller to reach a Docker socket; see [Docker
profiles](#docker-execution-profiles) below.

The image carries no `HEALTHCHECK`, because only the control-plane role listens
on HTTP and a worker container would report permanently unhealthy against any
HTTP probe. Configure readiness where you start the controller, against
`PEEN_HTTP_LISTEN_ADDRESS`:

```yaml
healthcheck:
  test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8080/healthz"]
  interval: 10s
  timeout: 3s
  retries: 5
  start_period: 30s
```

### Durable state and mounts

Peen keeps two directories apart. `PEEN_CONFIG_DIR` holds `AGENTS.md` and the
harness files, and every Docker worker receives it read-only. `PEEN_STATE_DIR`
holds the SQLite database, the audit logs, and the worker sockets, and no worker
receives it. Peen refuses to start when the two are the same path or one sits
inside the other, because the read-only configuration mount would otherwise
carry the database into every worker.

The process working directory is the default allowed workspace root, and a
session's canonical workspace path is its durable key. A Docker controller that
may launch Docker workers must mount host paths at the same literal paths inside
it. The host Docker daemon resolves worker mounts and cannot see a made-up path
such as `/workspace` that exists only inside the controller container.

```bash
root="$PWD"
config="$root/data/peen/config"
state="$root/data/peen/state"
workspace="$root/workspace"
mkdir -p "$config" "$state" "$workspace"

docker run --rm \
  --user "$(id -u):$(id -g)" \
  --env-file .env \
  -e PEEN_CONFIG_DIR="$config" \
  -e PEEN_STATE_DIR="$state" \
  -e PEEN_HOST_USERNAME="$(id -un)" \
  -e PEEN_HOST_HOME="$HOME" \
  -p 8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace:$workspace" \
  -w "$workspace" \
  peen run
```

The command overrides the example's placeholder directories with the literal
host paths it mounts. `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` tell a
controller running under numeric `--user` IDs how a Docker worker should name
the recreated host account. `-w` selects the workspace. Change all three paths
together when your project lives elsewhere.

Two mount requirements matter here:

- Your own UID and GID own the created directories, so Peen can set `0700` on
  `PEEN_STATE_DIR` and `0600` on `peen.db`, and agent-written files remain
  yours.
- The configuration, state, and workspace sources must exist before `docker
  run`. Docker otherwise creates a root-owned empty source, which is almost
  never the project you meant to hand to an agent.
- Serving several projects from one Peen needs `PEEN_WORKSPACE_ROOTS`. Mount
  each project at its literal host path, then list the allowed roots as a JSON
  array of those same paths. A client may open any directory under a root. See
  [workspace roots](configuration.md#workspace-roots).

### Networking and the API token

Publish whatever `PEEN_HTTP_LISTEN_ADDRESS` binds to (`-p 8080:8080` for the
default `:8080`). If you expose the container beyond a trusted network, set
`PEEN_API_TOKEN` to a real value; it is empty, and therefore unauthenticated,
by default. See [authentication](../README.md#things-worth-knowing).

Metrics bind only to `PEEN_METRICS_LISTEN_ADDRESS`, which defaults to the
container's loopback interface at `127.0.0.1:9090`. Do not publish it with
`-p` and assume it is reachable from the host. Run the scraper in the same
network namespace as Peen, or collect metrics through an operator-controlled
local proxy that shares that namespace.

### Docker execution profiles

A Docker execution profile runs a session's worker in a sibling container rather
than as a child process of the controller. The controller needs its own Docker
socket to create one. It resolves that socket from `PEEN_DOCKER_SOCKET`, then a
`unix://` `DOCKER_HOST`, then `/var/run/docker.sock`, and decides at startup
whether it has that authority. Without it, opening a session on a Docker profile
is refused. Peen does not quietly run the worker natively instead.

A socket it can reach but cannot use degrades the same way. The controller logs
the reason once at startup and keeps serving every other profile, rather than
refusing to start over a capability those sessions may never ask for. The usual
cause is a controller run with numeric `--user uid:gid` against an image that
holds no account for that UID; set `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` to
resolve it.

Mounting the host Docker socket into the controller is what grants that
authority, and it is host-root-equivalent. Do it only when you want sibling
worker containers, and keep the controller itself behind the restrictions in the
next section:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" \
  --group-add "$(stat -c '%g' /var/run/docker.sock)" \
  --env-file .env \
  -e PEEN_CONFIG_DIR="$config" \
  -e PEEN_STATE_DIR="$state" \
  -e PEEN_HOST_USERNAME="$(id -un)" \
  -e PEEN_HOST_HOME="$HOME" \
  -p 8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace:$workspace" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -w "$workspace" \
  peen run
```

Worker containers are named `peen-worker-<session-uuid>` and carry exactly two
labels, `peen.managed=true` and `peen.session=<session-uuid>`. Peen stops or
removes a container only while both labels still match the session it recorded,
so it never touches a container it did not create. A worker mounts the workspace
writable at its literal host path, the configuration directory read-only at its
literal path, and its own session socket directory writable, and runs as the
controller's own host UID, GID, and username, so files it writes keep host
ownership and a path means the same thing on both sides of the boundary.

Three things are deliberately absent from a worker: `PEEN_STATE_DIR`, so the
controller's database and audit logs are unreachable; the worker socket root, so
one worker never learns another session's socket path; and any image from a
repository other than `psyb0t/peen`.

The worker gets its own runtime settings and the named provider credentials for
its model calls. It does not get the controller API token or controller Docker
socket. A profile can independently grant a worker Docker socket, but that is a
host-root-equivalent grant to the agent and must be deliberate.

`make test-docker-worker-source` builds the Dockerfile in the current checkout,
then creates one worker container, replaces the worker command with a probe,
and checks what the dropped process can see. It runs no agent, model, or
controller database. `make test-docker-worker
PEEN_DOCKER_WORKER_TEST_IMAGE=psyb0t/peen:<tag>` runs the same probe against a
published image.

A profile's `allowDockerSocket` is a separate decision from the controller's
authority. It gives the worker itself access to the daemon, which is
host-root-equivalent for the model's tools. `GET /v1/execution-profiles` reports
such a profile with `hostRootEquivalent` true and a `capabilityWarning`.

A Docker worker starts as root only for entrypoint bootstrap. The entrypoint
creates or reconciles the controller's host UID, GID, and username, then drops
to that account before the agent runs. `allowPrivilegeEscalation` additionally
writes a passwordless sudo rule for that account and removes the worker's
`no-new-privileges` guard. See [privilege
escalation](configuration.md#privilege-escalation).

Peen builds each worker container itself from the named profile. The flags you
pass to `docker run` shape the controller, not its workers, so hardening the
controller does not carry over. A worker's mounts, network, socket, and
privileges come from `PEEN_EXECUTION_PROFILES` and nowhere else.

### Isolation is the operator's job, not the image's

The Ubuntu base and the non-root controller account are the only isolation Peen
ships with by default. `run_command` and the file tools still have whatever
access the selected worker UID has inside the container: everything under the
mounted `PEEN_CONFIG_DIR` and workspace, plus network access unless you
restrict it. Read the root README's [Security](../README.md#security)
section, and add `--network`, `--cap-drop`, `--read-only` (with explicit
writable mounts for the two directories above), or a seccomp/AppArmor
profile as your deployment needs.
