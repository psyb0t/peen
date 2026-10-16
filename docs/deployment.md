# Deployment

Install Peen with the installer unless you have a reason to own the process yourself. It pins one release, writes the configuration, and gives you the `peen` command that starts, stops, upgrades, and opens workspaces. The manual routes further down run the same server, but you then own the `docker run` flags or the process around it.

All launch methods use the same `PEEN_` configuration, trusted `PEEN_CONFIG_DIR` harness layer, and durable `PEEN_STATE_DIR` state.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

Read [install.sh](../install.sh) before piping it into a shell. With no flags it installs the newest release with `--control-space docker --agent-space docker`. Peen then runs in a container named `peen-<your user name>`, and each workspace's agent runs in its own container. [Where it runs](where-it-runs.md#the-four-combinations) explains the four combinations. Pass flags after `bash -s --`:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh |
  bash -s -- --control-space host --agent-space docker
```

The installer and the `peen` command need Linux and bash 4.4 or newer. I test them on Linux only. The docker control space and Docker agent spaces need a Docker daemon on the same machine that your user can use, through `/var/run/docker.sock` or a `unix://` `DOCKER_HOST`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--user` | yes | Put the `peen` command in `~/.local/bin`. Run it as yourself, not as root. |
| `--system` | no | Put the command in `/usr/local/bin` and the host binary in `/usr/local/lib/peen`. Run it with `sudo`. Peen is still set up for the user who ran `sudo`, because Peen always acts as a real user. |
| `--version vX.Y.Z` | the newest release | Install this release. The first release the installer supports is v0.20.0, and it refuses older ones. |
| `--rolling` | no | Follow the moving `psyb0t/peen:latest` image and the `main` branch instead of a release. |
| `--control-space host\|docker` | `docker` | Where Peen runs. `host` needs Linux on amd64 or arm64, plus `curl`, `jq`, and `tar`, and the installer refuses it without them. |
| `--agent-space control\|docker` | `docker` | Where each workspace's agent runs. `docker` needs a Docker daemon your user can use. |
| `--workspace-root DIR` | your home directory | The directory Peen may open workspaces in. Required with `--control-space docker --agent-space control`, because the agent can read and change all of it. |
| `--listen ADDR` | `127.0.0.1:8080` | Where Peen is reachable on this machine. `:8080` listens on every interface, so set `PEEN_API_TOKEN` first, because the API has no authentication without it. |
| `--container-name NAME` | `peen-<your user name>` | The controller container's name in the docker control space. |

The installer checks the combination before it changes anything. It refuses `--control-space host --agent-space control` when your user can reach a Docker daemon. It refuses a Docker agent space or control space when `docker info` fails. It then:

1. downloads the `peen` command for the release and puts it in `~/.local/bin/peen`, refusing to replace a file there that it did not install, and warns when that directory is not on your `PATH`;
2. creates `~/.config/peen` with `.env`, `config/`, and `state/`, all private to your user, or the directory in the `PEEN_HOME` environment variable when you set it before installing, which every later `peen` command must then also see;
3. pulls the release image when Docker is involved;
4. for `--control-space host`, takes the Peen binary out of the published image over the registry's HTTP API into `~/.local/lib/peen/peen`, checking each layer's sha256, so the host control space needs no Docker daemon;
5. writes `.env` with the image's own `peen config init`, filling in the directories, the workspace root, the default agent space, the pinned image, and your user name and home.

It does not start Peen. Add a model provider and its key first:

```bash
peen config edit
```

`.env` holds every setting [configuration](configuration.md) lists, with a comment above each. Keep the provider you use, set its model IDs, put its key in the named variable, and delete the provider you do not use together with its key variable. [Providers](configuration.md#providers) describes every field. Then open a project:

```bash
cd ~/work/my-app
peen
```

`peen` starts the controller when it is not running, opens a session for the current directory, and prints a link such as `http://127.0.0.1:8080/?session=<id>`, opening it in your browser when it can. The page shows that session after you connect. A directory outside the workspace root is refused with the command that widens the root.

### The peen command

| Command | What it does |
| --- | --- |
| `peen`, `peen .`, `peen <dir>`, `peen open [dir]` | Open a directory, default the current one, as described above. `peen <dir>` takes any argument that holds a `/` or is `.` or `..`. |
| `peen start`, `peen stop`, `peen restart` | Run the controller in the background. The docker control space starts the container with `--restart unless-stopped`, so it comes back after a reboot. The host control space starts the binary detached, logs to `state/controller.log`, and does not come back after a reboot. |
| `peen status` | Show the control space, default agent space, pinned image, workspace root, URL, whether the controller runs and answers, and the worker containers. Exits 3 when the controller is stopped. |
| `peen logs [-f] [N]` | Show the last N controller log lines, default 100, and follow them with `-f`. |
| `peen run [dir]` | Run a foreground controller whose workspace root is DIR, default the current directory, instead of `PEEN_WORKSPACE_ROOT`, for that run only. Ctrl-C stops it. Refused while the background controller runs. |
| `peen config edit`, `peen config show`, `peen config get KEY`, `peen config set KEY VALUE`, `peen config path` | Edit `.env`, print it with the API token and every provider key masked, read or write one setting, or print the file's path. `set` refuses a value with a line break, and refuses `PEEN_CONTROL_SPACE` and `PEEN_IMAGE`, which `peen setup` and `peen upgrade` own. Run `peen restart` after a change. |
| `peen setup [flags]` | Change the settings the installer chose. It takes the installer's `--control-space`, `--agent-space`, `--workspace-root`, `--listen`, `--container-name`, and `--rolling`, and checks the new combination the same way. Run `peen stop` before changing the control space or container name, and `peen restart` after any other change. |
| `peen doctor` | Check file modes, the control space's binary or image, Docker access against the socket rule, provider keys still set to `REPLACE_ME`, the workspace root, and whether the controller and `GET /api/v1/models` answer. Exits 1 on any failed check. |
| `peen upgrade [--rolling]` | Pin the newest release, pull its image or download its binary, replace the `peen` command with that release's copy, restart a running controller, and remove the old image. It moves `PEEN_WORKER_IMAGE` to the new release only when it still names the old one. |
| `peen uninstall [--keep-data\|--delete-data]` | Stop and remove the controller, the `peen` command, the binary, and the pinned image. It asks whether to delete `~/.config/peen` unless a flag answers. |
| `peen session ...`, `peen control status` | Run the server's own client commands, described in the [README](../README.md#drive-it-from-the-command-line), with this installation's configuration. `peen session` starts the controller first. |
| `peen version`, `peen help` | Print the installed command, image, and control space, or the help. |

The `peen` command stops or removes only a container whose `peen.install` label names this installation's configuration directory. A container with the same name and any other label is reported and left alone. It never passes `--privileged`.

In the docker control space the controller runs as your UID and GID with `.env` as its `--env-file`. The configuration directory, the state directory, and the workspace root are mounted at their literal host paths. The API listens on the `--listen` address. With `--agent-space docker` the controller also gets the host Docker socket at its literal path and the socket's group. That is the setup [Docker agent spaces](#docker-agent-spaces) describes by hand.

The host control space starts the binary with only `HOME`, `PATH`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TZ`, `DOCKER_HOST`, and the contents of `.env` in its environment, so secrets exported in your shell do not reach a `control` agent.

Before 1.0 a release can refuse a database an earlier release created. When `peen upgrade` cannot start the new release, it prints the controller's error, which names the files to delete. See [durable state and mounts](#durable-state-and-mounts).

## Install from source

```bash
git clone https://github.com/psyb0t/peen.git
cd peen
make install
```

This builds `./build/peen` and copies it to `PREFIX`, which defaults to `~/bin`. Pass `PREFIX` for anywhere else, with `sudo` when the destination needs it:

```bash
sudo make install PREFIX=/usr/local/bin
```

The target warns when `PREFIX` is not on your `PATH`. This installs the server binary, not the `peen` command from the installer, so `start`, `open`, `setup`, `upgrade`, and the other management commands above do not exist here. The binary has its own `session` and `control` commands, which the [README](../README.md#drive-it-from-the-command-line) describes. Start the server with its own `run` subcommand as [Build from source](#build-from-source) shows. Both programs are named `peen`. If you use the installer as well, install the server binary under another `PREFIX` or another name, or whichever directory comes first in your `PATH` decides which one runs.

## Build from source

```bash
make build
```

This runs a pinned `golang:1.26.9-alpine` Docker image to produce a static, `CGO_ENABLED=0` Linux binary at `./build/peen`. No local Go toolchain is required.

Do not source `.env` before a bare run. It is Docker `--env-file` syntax, and the raw JSON in `PEEN_UPSTREAMS` is not Bash syntax. Set the values through your shell or process manager instead:

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

A bare run uses the `control` agent space, so the agent has the access of your user. If your user can use a Docker daemon, for example because it is in the `docker` group, Peen refuses `control` and runs each workspace in its own container instead. A source build reports the version `dev`, so it needs an image for those containers. Build one with `make docker-build` and add `export PEEN_WORKER_IMAGE=peen` before `run`. See [where it runs](where-it-runs.md#the-docker-socket-rule).

If you have a matching local Go toolchain and prefer to skip Docker for the build step itself:

```bash
CGO_ENABLED=0 go build -o ./build/peen ./cmd
./build/peen run
```

However you build it, the program calls itself `peen` in its own `--help` and in the `binary` field of every log line. No build flags are needed for that.

## go install

```bash
go install github.com/psyb0t/peen/cmd@latest
```

The module's single main package lives at `cmd/`, so the installed file is named `cmd`, not `peen`. Rename it if you want the conventional name:

```bash
mv "$(go env GOPATH)/bin/cmd" "$(go env GOPATH)/bin/peen"
```

Either filename runs the same program, `run` is still the subcommand that starts the service, and it still reports itself as `peen`.

## Docker

This section runs the published image by hand. These are the `docker run` commands the `peen` command builds for the docker control space, written out so you can put them in Compose, systemd, or another process manager. The commands are for Linux. Pick a release and use it everywhere below:

```bash
image="psyb0t/peen:vX.Y.Z"
```

To run your own build instead, run `make docker-build` in a checkout, which tags the image `peen`, and set `image=peen`.

The build stage compiles the same static binary as `make build`. The runtime stage is Ubuntu 24.04, pinned by image digest, with `ca-certificates`, `curl`, `git`, `jq`, `less`, `ripgrep`, `sudo`, `tini`, `tzdata`, `unzip`, and `util-linux` installed. `sudo` authorizes nobody by default. The image ships no sudoers rule, and only a worker on a Docker profile with `sudo: true` writes one for its own account. `util-linux` supplies the `setpriv` the entrypoint drops privileges with. The image is Ubuntu because `runCommand` needs a real shell and utilities, so the runtime image is the agent's toolbox. A distroless or scratch image would leave the agent with nothing to run commands with.

The image defaults to a fixed non-root user, UID and GID `10001` (`appuser`), under `tini` as PID 1, so processes `runCommand` spawns get reaped instead of becoming zombies. The default command is `run`, which starts a controller. When that controller works with host bind mounts or launches Docker workers, run it as your host UID and GID as shown below.

The same image is also the worker image. For a session in a Docker agent space, a controller creates `peen-worker-<session-uuid>` from it with the `worker` command and hands that worker its launch document on stdin. That needs the controller to reach a Docker socket, as [Docker agent spaces](#docker-agent-spaces) below describes. [Where it runs](where-it-runs.md) explains the agent spaces and the four combinations of control space and agent space.

The image carries no `HEALTHCHECK`, because only the controller listens on HTTP and a worker container would report unhealthy against any HTTP probe forever. Configure readiness where you start the controller, against `PEEN_HTTP_LISTEN_ADDRESS`:

```yaml
healthcheck:
  test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8080/healthz"]
  interval: 10s
  timeout: 3s
  retries: 5
  start_period: 30s
```

### Durable state and mounts

Peen keeps two directories apart. `PEEN_CONFIG_DIR` holds `AGENTS.md` and the harness files, and every Docker worker receives it read-only. `PEEN_STATE_DIR` holds the session database `peen.db`, the workflow database `workflows.db`, the audit logs, and the worker sockets, and no Docker worker receives it. A `control` worker runs as the controller's user and reaches both. Peen refuses to start when the two are the same path or one sits inside the other, because the read-only configuration mount would otherwise carry the database into every Docker worker.

Before 1.0 Peen refuses an earlier database when its schema differs from the current schema. On open, Peen compares the database's schema with the schema its own migrations build, and refuses to start when they differ, with an error that names the files to delete. Stop Peen, delete `peen.db`, `peen.db-wal`, `peen.db-shm`, `workflows.db`, `workflows.db-wal`, and `workflows.db-shm` from `PEEN_STATE_DIR`, and start it again to create new databases. A leftover `peen.db-wal` next to a new `peen.db` makes SQLite try to replay it into the wrong file. Keeping `workflows.db` still starts, but leaves the deleted sessions' workflow instances orphaned in it. Deleting the databases discards every stored session, transcript, job record, model call, and workflow history, so copy the six files first if you want to keep the history.

For a consistent backup, stop Peen and copy the entire state directory, including both SQLite databases and any `-wal` or `-shm` files. Restore the files together. Copying only `peen.db` loses workflow scheduling history.

A session's workspace path is its durable key. A Docker controller that may launch Docker workers must mount host paths at the same literal paths inside itself, because the host Docker daemon resolves worker mounts and cannot see a path such as `/workspace` that exists only inside the controller container.

Create the three directories, then let the image write a commented `.env` for them with `peen config init`:

```bash
root="$HOME/.local/share/peen"
config="$root/config"
state="$root/state"
workspace_root="$HOME/work"
mkdir -p "$config" "$state" "$workspace_root"

docker run --rm --network none \
  --user "$(id -u):$(id -g)" \
  -v "$root:$root" \
  "$image" config init \
  --env-file "$root/.env" \
  --config-dir "$config" \
  --state-dir "$state" \
  --workspace-root "$workspace_root" \
  --worker-image "$image" \
  --host-username "$(id -un)" \
  --host-home "$HOME"

${EDITOR:-vi} "$root/.env"
```

`config init` never overwrites an existing file. Set the provider section in `.env` as [providers](configuration.md#providers) describes, then start the controller:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" \
  --env-file "$root/.env" \
  -p 127.0.0.1:8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace_root:$workspace_root" \
  -w "$workspace_root" \
  "$image" run
```

This runs the `control` agent space, because no Docker socket is mounted. `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` in `.env` tell a controller running under numeric `--user` IDs which host account a Docker worker should recreate. `-w` and `PEEN_WORKSPACE_ROOT` both name the workspace root, and each project directory inside it opens as its own workspace. Change the paths together when your projects live elsewhere.

Three mount requirements matter here:

- Your own UID and GID own the created directories, so Peen can set `0700` on `PEEN_STATE_DIR` and `0600` on `peen.db`, and files the agent writes stay yours.
- The configuration, state, and workspace sources must exist before `docker run`. Docker otherwise creates an empty source owned by root, which is almost never the project you meant to hand to an agent.
- To serve several projects from one Peen, mount the directory that holds them at its literal host path and make it the workspace root. A client may then open any project directory inside it as its own workspace. See [workspace root](configuration.md#workspace-root).

### Networking and the API token

Publish the port `PEEN_HTTP_LISTEN_ADDRESS` binds to inside the container, `8080` by default. `-p 127.0.0.1:8080:8080` reaches it from this machine only. If you publish it beyond a trusted network, set `PEEN_API_TOKEN` to a real value first. It is empty by default, and an empty token turns authentication off. See [Things worth knowing](../README.md#things-worth-knowing).

Metrics bind only to `PEEN_METRICS_LISTEN_ADDRESS`, which defaults to the container's loopback interface at `127.0.0.1:9090`. Publishing it with `-p` does not make it reachable from the host. Run the scraper in the same network namespace as Peen, or collect metrics through a local proxy you run in that namespace.

### Docker agent spaces

A Docker agent space runs a session's worker in a sibling container rather than as a child process of the controller. The controller needs its own Docker socket to create one, and decides at startup whether it has one, as [the Docker socket rule](where-it-runs.md#the-docker-socket-rule) describes. Without it, a session in a Docker agent space is refused. Peen does not quietly run the worker in the `control` agent space instead.

A controller that can reach a Docker daemon refuses the `control` agent space, so the default becomes `docker:default` and every workspace gets its own container. [Where it runs](where-it-runs.md#the-docker-socket-rule) explains why.

Because the Docker agent spaces are then the only ones offered, Peen refuses to start when it reaches a daemon but cannot prepare to start worker containers. The usual cause is a controller run with numeric `--user uid:gid` without `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`, which the error names and `config init` above writes. A daemon reachable only through a `tcp://` `DOCKER_HOST` also stops startup, because Peen launches workers only through a local socket.

Mounting the host Docker socket into the controller grants that authority, and it is host root for anything that can use the socket. Do it only when you want sibling worker containers, and keep the controller itself behind the restrictions in the next section. The container has no Docker daemon of its own and no `--privileged`. `stat -c` below is the Linux form:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" \
  --group-add "$(stat -c '%g' /var/run/docker.sock)" \
  --env-file "$root/.env" \
  -p 127.0.0.1:8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace_root:$workspace_root" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -w "$workspace_root" \
  "$image" run
```

`PEEN_WORKER_IMAGE`, which `config init --worker-image` wrote, makes every worker run the same image as the controller. Clear it to let a Docker profile's own `image` apply, as [Docker profiles](where-it-runs.md#docker-profiles) describes.

Worker containers are named `peen-worker-<session-uuid>` and carry two labels, `peen.managed=true` and `peen.session=<session-uuid>`. Peen stops or removes a container only while both labels still match the session it recorded, so it never touches a container it did not create. A worker mounts the workspace writable at its literal host path, the configuration directory read-only at its literal path, and its own session directory writable. It runs as the controller's own host UID, GID, and username, so files it writes keep host ownership and a path means the same thing on both sides.

Three things never reach a worker: `PEEN_STATE_DIR`, so the controller's database and audit logs are unreachable; the root of the worker socket directories, so one worker never learns another session's socket path; and an image chosen by a client or the model. The operator picks the image with `PEEN_WORKER_IMAGE` or the Docker profile's `image`.

The worker gets its own runtime settings and the named provider credentials for its model calls. It does not get the controller API token or the controller's Docker socket. Commands the agent runs start without the provider credentials in their environment. A Docker profile can grant a worker the Docker socket with `docker: full`, which is host root for the agent, or a read-only Docker API with `docker: readonly`. [Docker access](configuration.md#docker-access) says how each reaches the worker.

`make test-docker-worker-source` builds the Dockerfile in the current checkout, creates one worker container, replaces the worker command with a probe, and checks what the process can see after it drops privileges. It runs no agent, model, or controller database. `make test-docker-worker PEEN_DOCKER_WORKER_TEST_IMAGE=psyb0t/peen:<tag>` runs the same probe against a published image.

A profile's `docker: full` is a separate decision from the controller's own access. It gives the worker itself access to the daemon, which is host root for the model's tools. `GET /api/v1/agent-spaces` reports such a space with `hostRootEquivalent` true and a sentence in `capabilityWarnings`.

A Docker worker starts as root only to set up its account. The entrypoint creates or updates the controller's host UID, GID, and username, then drops to that account before the agent runs. `sudo: true` also writes a passwordless sudo rule for that account and removes the worker's `no-new-privileges` guard. See [sudo](configuration.md#sudo).

Peen builds each worker container itself from the Docker profile. The flags you pass to `docker run` shape the controller, not its workers, so hardening the controller does not carry over. A worker's mounts, network, Docker access, and sudo come from `PEEN_DOCKER_PROFILES` and nowhere else.

A Docker worker calls the model provider itself. A profile with `network: none` runs the worker with networking disabled, so it cannot reach a provider over the network. The default, `bridge`, can.

### Isolation is the operator's job, not the image's

The Ubuntu base and the non-root controller account are the only isolation Peen ships with by default. `runCommand` and the file tools have whatever access the worker's account has, which covers everything under the mounted `PEEN_CONFIG_DIR` and workspace, plus network access unless you restrict it. Read the README's [Security](../README.md#security) section. Where to harden depends on the session's agent space:

- In the `control` agent space the agent runs inside the controller container, so harden that container. Add `--network`, `--cap-drop`, `--read-only` (with writable mounts for the state directory and the workspace root, and the configuration directory mounted read-only), or a seccomp or AppArmor profile as your deployment needs. Do not mount the Docker socket into it, because Peen refuses `control` beside a reachable socket.
- In a `docker:<profile>` agent space the agent runs in its own worker container, and `docker run` flags affect only the controller. Harden the Docker profile instead, with `network`, `mounts`, `docker`, and `sudo`. See [Docker profiles](where-it-runs.md#docker-profiles).

The controller and its workers mark themselves non-dumpable when they start, so a command the agent runs cannot read `PEEN_API_TOKEN` or a provider key from their `/proc/<pid>/environ` or memory. Root and any process with `CAP_SYS_PTRACE` still can, so keep the controller on its non-root account and do not start it with `--cap-add SYS_PTRACE` or `--privileged`. Non-dumpable does not protect files. A `control` agent can still read and change `PEEN_STATE_DIR`, which holds every session's transcript in `peen.db`. See [where it runs](where-it-runs.md#agent-spaces).
