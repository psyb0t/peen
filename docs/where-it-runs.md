# Where it runs

Two settings decide what an agent can touch. You choose both when you install, and the `peen` command records them in `~/.config/peen/.env`.

- The control space is where Peen itself runs. `host` runs the Peen binary on your machine, and `docker` runs it in a container. It is `--control-space` in the installer and `PEEN_CONTROL_SPACE` in `.env`.
- The agent space is where each workspace's agent runs. `control` runs it in the same place as Peen, and `docker` gives each workspace its own container. It is `--agent-space` in the installer and `PEEN_DEFAULT_AGENT_SPACE` in `.env`, where `docker` is written `docker:default`.

A workspace is a project directory you open with `peen` or in the control surface. Each workspace gets one durable session and one agent.

## The four combinations

1. `--control-space host --agent-space control`: Peen and the agent run on your machine as your user. The agent can read and change everything your user can, including `.git/`, `.env` files, and SSH keys. This combination needs no Docker, and Peen refuses it when your user can reach a Docker daemon, for example as a member of the `docker` group, because the agent could use that daemon to become root. See [the Docker socket rule](#the-docker-socket-rule). To use it on a machine with Docker, install Peen as a user that is not in the `docker` group.
2. `--control-space host --agent-space docker`: Peen runs on your machine, and each workspace's agent runs in its own container that sees only that workspace, the configuration directory read-only, and whatever its Docker profile adds.
3. `--control-space docker --agent-space control`: Peen runs in a container, and the agent runs inside that container with it. The agent sees only the folders mounted into the container: the workspace root, Peen's configuration directory, and Peen's state directory. The installer requires `--workspace-root` here, because that directory is everything the agent can read and change.
4. `--control-space docker --agent-space docker`: Peen runs in a container with the host Docker socket mounted, and each workspace's agent runs in its own sibling container. This is the installer's default. Peen itself gets the socket, which is host root for Peen, and the agents do not.

To change a setting later, run `peen setup` with the new flag and then `peen restart`, for example `peen setup --agent-space docker && peen restart`. Run `peen stop` first when you change the control space.

[Deployment](deployment.md#install) has the installer's flags and the `peen` commands. The rest of this page explains each agent space in detail.

## Agent spaces

An agent space is the place a workspace's agent runs. There are two kinds.

- `control` runs the agent where Peen runs, as a child process of the controller. It has the controller's user, files, and groups. It is built in, takes no settings, and needs no image.
- `docker:<profile>` runs the agent in its own container, one per workspace, named `peen-worker-<session-uuid>`. The container sees the workspace, the configuration directory read-only, a private directory for its session, and whatever the Docker profile adds. The session directory holds the socket the worker talks to Peen through and the directory [hooks](hooks.md) may keep state in. The profile name after the colon picks the profile, for example `docker:default` or `docker:ops`.

A client picks an agent space by name when it opens a workspace, and never sends an image, mount, network setting, or capability. Those come from the operator's configuration alone. `PEEN_DEFAULT_AGENT_SPACE` names the agent space for a workspace opened without a choice, and `PEEN_DOCKER_PROFILES` defines the Docker profiles. See [configuration](configuration.md#agent-spaces) for the variables.

What `control` can touch depends on where Peen runs:

- Peen on the host: everything your user can touch, because the file tools and `runCommand` have no path allowlist and no approval step.
- Peen in a container with no Docker daemon in reach: the mounted folders. Those include `PEEN_CONFIG_DIR`, which the agent can write, and `PEEN_STATE_DIR`, which holds every session's transcript in `peen.db`. Outside the container it reaches whatever the container's network reaches, which by default includes your LAN and the internet.
- Peen anywhere it can reach a Docker daemon: host root. This is why Peen refuses `control` there. See [the Docker socket rule](#the-docker-socket-rule).

Commands the agent runs, in any agent space, start with an environment that has `PEEN_API_TOKEN` and every variable named by an upstream's `apiKeyEnv` removed. A `control` worker itself never receives `PEEN_API_TOKEN`, `PEEN_STATE_DIR`, the controller's Docker settings, or any other `PEEN_` setting a worker does not read. It keeps the provider credentials, because it makes the model calls. Variables that are not Peen settings, such as `PATH` and `HOME`, pass through to the agent's commands unchanged. The `peen` command starts a host controller with only `HOME`, `PATH`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TZ`, `DOCKER_HOST`, and the contents of `.env`, so secrets exported in your shell do not reach the agent. If you start the binary yourself, do not start it with unrelated secrets in its environment.

On Linux the controller and every worker mark themselves non-dumpable when they start. The kernel then refuses a command that runs as the same user when it tries to read their `/proc/<pid>/environ`, their memory, or attach to them with ptrace, so a `control` agent cannot take the API token or a provider key from either process. Commands the agent starts are ordinary processes and stay readable to each other.

A `control` agent still runs as the controller's user, and that leaves these limits:

- It can read and change `PEEN_STATE_DIR`, which holds `peen.db` with every session's transcript, and `PEEN_CONFIG_DIR`.
- It can read any file or other process of that user that holds a secret, such as an `.env` file or the shell you exported `PEEN_API_TOKEN` in.
- Non-dumpable does not stop root or a process with `CAP_SYS_PTRACE`. Do not run the controller as root or grant it that capability.

A Docker agent space is the isolation boundary. Use one when an agent must not see other sessions.

## Workspaces that would expose Peen's own directories

Peen refuses to open a workspace that is, contains, or sits inside `PEEN_STATE_DIR`, and a workspace that is or contains `PEEN_CONFIG_DIR`. The same applies to `PEEN_WORKER_SOCKET_DIR` and `PEEN_LOG_DIRECTORY` when you move them out of the state directory. The request returns `403 WORKSPACE_NOT_ALLOWED` and creates no session.

A workspace is mounted writable into its Docker worker. With a workspace root of `$HOME` and state under `~/.config/peen/state`, opening `~` or `~/.config` as a workspace would otherwise hand that worker the database with every session's transcript and every other session's worker socket. Open the project directories inside your home instead, such as `~/work/my-app`.

## Docker profiles

A Docker profile shapes the container behind a `docker:<profile>` agent space. `PEEN_DOCKER_PROFILES` is a JSON array of them:

```bash
peen config set PEEN_DOCKER_PROFILES '[{"name":"ops","network":"host","docker":"readonly","mounts":[{"source":"/srv/datasets","readOnly":true}]},{"name":"builder","docker":"full","sudo":true}]'
peen config set PEEN_DEFAULT_AGENT_SPACE docker:ops
peen restart
```

`peen config set` writes each value on one line of `.env`, which is a Docker `--env-file`, so the JSON goes in without outer quotes. Without the installer, write the same two lines into your `.env`. The second line makes `docker:ops` the agent space for every workspace opened without a choice.

Every option is optional except `name`. `docker` defaults to `none` and `sudo` to `false`. `network` defaults to `bridge`, so the agent can reach its model provider and anything else that network reaches. Set `"network": "none"` to cut it off.

| Option | Default | Meaning |
| --- | --- | --- |
| `name` | required | Up to 64 letters, digits, dots, underscores, and hyphens, starting with a letter or digit. The agent space is `docker:<name>`. |
| `image` | the image published alongside this build | Any image reference the Docker daemon can resolve. Peen uses `PEEN_WORKER_IMAGE` when it is set, then the profile's `image`, then `psyb0t/peen` tagged with the controller's version. The installer sets `PEEN_WORKER_IMAGE` to the release it installed, which overrides every profile's `image`. To let profile images apply, clear it with `peen config set PEEN_WORKER_IMAGE ''` and run `peen restart`. `peen upgrade` then leaves it empty. Any image must contain the same Peen release as the controller, because the controller and its workers talk a protocol that changes between releases, so build a custom image `FROM psyb0t/peen:<the controller's version>`. See [configuration](configuration.md#docker-workers). |
| `mounts` | none | Extra host paths for the container. An array of `{"source": "<absolute host path>", "target": "<absolute path in the container>", "readOnly": true or false}`. `target` is optional and defaults to the source path. |
| `docker` | `none` | How much of the host Docker daemon the agent reaches. `none` gives it nothing. `readonly` gives it a Docker API that answers only read requests, described [below](#docker-readonly). `full` mounts the controller's Docker socket at `/var/run/docker.sock` in the container and adds the socket's group to the agent's account, which is host root. |
| `network` | `bridge` | The container's network. `bridge` is Docker's default network. `none` disables networking, which also stops the agent from calling its model provider. `host` shares the host's network, so the agent can reach every service the host can. |
| `sudo` | `false` | `true` gives the agent passwordless sudo inside the container and removes the container's `no-new-privileges` guard. |
| `revision` | `0` | A number you raise by hand when you change the profile. Peen stores it with every worker it starts, which `GET /v1/session/workers` lists, so the record shows which definition ran. Peen does not check it. |

A profile named `default` always exists with no Docker access, the `bridge` network, and no sudo. Defining a profile named `default` replaces the built-in one, which is how you give the default agent space an image, a mount, or Docker access. Defining a key Peen does not know, such as the removed `allowNetwork`, stops startup.

### `docker: readonly`

The agent gets a Docker API at a socket in its own session directory, and `DOCKER_HOST` in its environment points there, so `docker ps`, `docker inspect`, `docker images`, and Docker SDKs work with no setup. The controller runs one proxy per worker and forwards to its own Docker daemon. The worker never receives the real socket.

The proxy answers only `GET` and `HEAD` requests to these endpoints: ping, version, info, events, disk usage, the container, image, network, and volume lists, container and image inspect, network and volume inspect, and container stats. It answers everything else with `403` and never passes it to the daemon. That includes creating, starting, stopping, or removing anything, `exec`, `attach`, container logs, `top`, file copies and exports, image pulls, pushes, and builds, and the Swarm, plugin, and secret endpoints. Container and image inspect output keeps each environment variable's name and replaces its value with `<redacted>`.

The agent can still see every container, image, network, and volume on the host, their configuration apart from environment values, their resource use, and the daemon's event stream. `readonly` is not host root, so `GET /v1/agent-spaces` reports it with `hostRootEquivalent` false and one sentence in `capabilityWarnings`.

### `docker: full`

`docker: full` mounts the socket the controller itself uses, the one [the Docker socket rule](#the-docker-socket-rule) found. The Docker daemon resolves that path on the host, so a controller in a container must mount the socket at its literal host path, which the `peen` command does. Peen grants the socket's owning group to the agent's account, so the agent reaches the daemon without running as root. A socket owned by group 0 cannot be granted that way, and Peen refuses to start while any profile sets `docker: full` beside one.

### Grants Peen warns about

Four grants widen what the agent can reach, and Peen says so. `docker: full` is host root, `docker: readonly` shows every container on the host, `network: host` removes the network boundary, and `sudo: true` gives the agent root inside its container. `GET /v1/agent-spaces` reports each Docker space with its grants, a `hostRootEquivalent` flag, and one sentence per such grant in `capabilityWarnings`. The control surface shows the grants next to each name, for example `ops (docker: readonly, network: host, sudo: no)` and `builder (docker: full = host root, network: bridge, sudo: yes)`. You see them in the dropdown where you open a workspace and in the form that changes a session's agent space.

Peen reads `PEEN_DOCKER_PROFILES` at startup, so a profile change needs `peen restart`. A restart stops every worker, and each session's next turn starts a worker under the new definition.

## Choosing an agent space for a workspace

A workspace gets its agent space once, when its session is created, and keeps it. `peen` creates the session in the default agent space. To create it in another one, open the workspace for the first time in the control surface and choose an agent space in the "Agent space" dropdown, or send `agentSpace` to `POST /v1/sessions/open`. Opening a workspace that already has a session never moves it, so for a workspace you opened before, use the move described below. Peen still checks the name on every open. A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED` and creates nothing, even for a workspace that already has a session.

Moving a session later is `POST /v1/session/reconfigure`, or the "Change agent space" form in the details panel. It needs a reason, refuses a session with a turn in flight, stops the session's current worker, and records the move in the session's agent space history, which `GET /v1/session/agent-space-decisions` returns. The next turn starts a new worker in the new agent space. The [API reference](http-api.md#get-v1agent-spaces) has the request and response shapes.

A session keeps the agent space it was opened in. The deployment can stop offering it later, because you removed the profile or because the controller can now reach a Docker daemon and `control` is gone. Every turn in that session then fails with `AGENT_SPACE_NOT_ALLOWED` until you move it with `POST /v1/session/reconfigure`.

## The Docker socket rule

Peen decides at startup whether the controller can reach a Docker daemon. It checks every candidate, not only the first one that is set:

- the path in `PEEN_DOCKER_SOCKET`;
- the path in `DOCKER_HOST` when it is a `unix://` address;
- `/var/run/docker.sock`;
- the address in `DOCKER_HOST` when it is a `tcp://` address.

A socket counts when the Docker API answers `GET /_ping` on it. A missing path, a stale socket file, or a socket your user is not allowed to open does not count. A `tcp://` address counts when it accepts a connection. Setting `PEEN_DOCKER_SOCKET` to another path does not hide the default socket. Peen does not detect a daemon behind an `ssh://` `DOCKER_HOST`, so do not set one in a controller that runs `control` sessions.

- With a daemon in reach, `control` is never offered. `PEEN_DEFAULT_AGENT_SPACE=control` stops startup. With nothing set, the default agent space is `docker:default`.
- Without one, `control` is offered and is the default. Docker agent spaces are listed, and a session in one fails at its first turn with a message that the controller has no Docker socket. Peen never runs it as `control` instead, because the profile was a promise of isolation.

The reason is that a control worker is a child process with the controller's user, files, and groups. If the controller can use a Docker daemon, so can its agent, and one `curl --unix-socket` call to the Docker API starts a container that mounts the host's root directory. A reachable daemon therefore turns `control` into host root, and no setting lets Peen offer `control` beside one. Use a Docker profile instead, and give it `docker: full` only for a workspace where you accept host root.

The `peen` command applies the same rule before it changes anything. `peen setup --control-space host --agent-space control` fails when your user can reach a Docker daemon, and `peen doctor` reports an installation that has drifted into that state.

The controller's refusal reads:

```text
PEEN_DEFAULT_AGENT_SPACE=control: this controller can reach the Docker daemon at /var/run/docker.sock, and a reachable daemon makes control sessions host root, so use Docker profiles instead, for example PEEN_DEFAULT_AGENT_SPACE=docker:default, or run the controller without the socket
```

With a daemon in reach, the Docker agent spaces are the only ones offered, so Peen refuses to start when it cannot prepare to start worker containers. The error names `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`, because the usual cause is a controller run with a numeric `--user uid:gid` that has no account in the image. The `peen` command sets both. Peen launches workers only through a local socket, so a daemon reachable only over `tcp://` also stops startup.

## Why startup was refused

Startup names the variable and the value it rejected, and `peen start` prints those lines. This section covers the agent space and Docker profile settings. Every other setting lists what it accepts in [configuration](configuration.md), and the most common startup failure is a provider key that is missing or still `REPLACE_ME`, which `peen doctor` reports. Besides the socket rule above, these agent space settings stop startup:

- two Docker profiles with one name;
- a profile name over 64 characters, one that does not start with a letter or digit, or one with a character other than letters, digits, dots, underscores, and hyphens;
- a mount `source` or `target` that is not an absolute path;
- a negative `revision`;
- a key Peen does not know, or a `docker` or `network` value outside the lists above;
- `docker: full` while the controller's socket is owned by group 0;
- anything after the JSON array in `PEEN_DOCKER_PROFILES`;
- a `PEEN_DEFAULT_AGENT_SPACE` that is not `control` or `docker:<profile>`, or that names a profile you did not define.

A default of `docker:<profile>` on a controller with no Docker daemon in reach passes startup, and every new session then fails at its first turn.

## Questions this page answers

- Where does my agent run? In the agent space of its workspace: `control` next to Peen, or `docker:<profile>` in its own container. `peen status` shows the default, and the session list and `GET /v1/session` report each session's `agentSpace`.
- What can it touch? See [the four combinations](#the-four-combinations), [Agent spaces](#agent-spaces) for `control`, and the profile's `mounts`, `docker`, `network`, and `sudo` for a Docker space.
- How do I give each workspace its own container? Install with `--agent-space docker`, or run `peen setup --agent-space docker && peen restart`. Every workspace then runs in a `docker:<profile>` container, `docker:default` unless you pick another. You cannot keep some workspaces on `control`, because a reachable daemon removes it for all of them.
- How do I give an agent Docker access? Set `docker: readonly` in its profile to let it list and inspect containers, or `docker: full` for the whole daemon, which is host root.
- Why was my setup refused? See [why startup was refused](#why-startup-was-refused) and the socket rule above.
