# Where it runs

Two questions decide what an agent can touch. Where does Peen itself run, and where does the agent for each workspace run? The second answer is the agent space. This page explains both, the Docker profiles that shape a container, and the rule that decides which agent spaces a controller offers.

## Agent spaces

An agent space is the place a workspace's agent runs. There are two kinds.

- `control` runs the agent where Peen runs, as a child process of the controller. It has the controller's user, files, and groups. It is built in, takes no settings, and needs no image.
- `docker:<profile>` runs the agent in its own container, one per workspace, named `peen-worker-<session-uuid>`. The container sees the workspace, the configuration directory read-only, its own session socket directory, which also holds the hooks' `stateDirectory`, and whatever the Docker profile adds. The profile name after the colon picks the profile, for example `docker:default` or `docker:ops`.

A client picks an agent space by name when it opens a workspace, and never sends an image, mount, network setting, or capability. Those come from the operator's configuration alone. `PEEN_DEFAULT_AGENT_SPACE` names the agent space for a workspace opened without a choice, and `PEEN_DOCKER_PROFILES` defines the Docker profiles. See [configuration](configuration.md#agent-spaces) for the variables.

What `control` can touch depends on where Peen runs:

- Peen on the host: everything your user can touch. That includes `.git/`, `.env`, and SSH keys, because the file tools and `runCommand` have no path allowlist and no approval step.
- Peen in a container with no Docker daemon in reach: the mounted folders. Those include `PEEN_CONFIG_DIR`, which the agent can write when it is mounted writable as in the README, and `PEEN_STATE_DIR`, which holds every session's transcript in `peen.db`. Outside the container it reaches whatever the container's network reaches, which by default includes your LAN and the internet.
- Peen anywhere it can reach a Docker daemon: host root. This is why Peen refuses `control` there. See [the Docker socket rule](#the-docker-socket-rule).

Commands the agent runs, in any agent space, start with an environment that has `PEEN_API_TOKEN` and every variable named by an upstream's `apiKeyEnv` removed. In `control` the agent still runs as the controller's user, so a command can read both values from the controller's process environment through `/proc`. With the API token the agent can read and drive every other session through the API. Treat a `control` agent as holding every secret the controller holds.

## Three ways Peen runs

Each item says how Peen starts and which agent spaces you get.

1. As a host binary, started by `peen run`. If your user cannot use a Docker daemon, the only agent space the controller runs is `control`, with your user's full access. If your user can, which is the case for anyone in the `docker` group on a host running Docker, the socket rule applies. Peen then refuses `control` and gives every workspace a Docker container.
2. In a container with no Docker socket mounted, as the README shows. `control` runs inside that container, so the agent sees only the folders you mounted, the controller's two directories among them. Docker agent spaces are listed but refused when a session tries to launch, because the controller has no way to start a container.
3. In a container with the host Docker socket mounted, as [deployment](deployment.md#docker-agent-spaces) shows. The controller starts one sibling container per workspace through the host Docker daemon. `control` is refused. There is no Docker daemon inside the controller container and no `--privileged`.

## Docker profiles

A Docker profile shapes the container behind a `docker:<profile>` agent space. `PEEN_DOCKER_PROFILES` is a JSON array of them:

```bash
PEEN_DOCKER_PROFILES='[
  {"name":"ops","network":"host",
   "mounts":[{"source":"/srv/datasets","readOnly":true}]},
  {"name":"builder","docker":"full","sudo":true}
]'
PEEN_DEFAULT_AGENT_SPACE=docker:ops
```

Every option is optional except `name`. `docker` defaults to `none` and `sudo` to `false`. `network` defaults to `bridge`, so the agent can reach its model provider and anything else that network reaches. Set `"network": "none"` to cut it off.

| Option | Default | Meaning |
| --- | --- | --- |
| `name` | required | Up to 64 letters, digits, dots, underscores, and hyphens, starting with a letter or digit. The agent space is `docker:<name>`. |
| `image` | the image published alongside this build | Any image reference the Docker daemon can resolve. Peen uses `PEEN_WORKER_IMAGE` when it is set, then the profile's `image`, then `psyb0t/peen` tagged with the controller's build version. See [configuration](configuration.md#docker-workers). |
| `mounts` | none | Extra host paths for the container. An array of `{"source": "<absolute host path>", "target": "<absolute path in the container>", "readOnly": true or false}`. `target` is optional and defaults to the source path. |
| `docker` | `none` | How much of the host Docker daemon the agent reaches. `none` mounts no socket. `full` mounts the controller's Docker socket at `/var/run/docker.sock` in the container and adds the socket's group to the agent's account, which is host root. `readonly` is reserved for a read-only socket proxy that does not exist yet, so a profile that names it stops startup. |
| `network` | `bridge` | The container's network. `bridge` is Docker's default network. `none` disables networking, which also stops the agent from calling its model provider. `host` shares the host's network, so the agent can reach every service the host can. |
| `sudo` | `false` | `true` gives the agent passwordless sudo inside the container and removes the container's `no-new-privileges` guard. |
| `revision` | `0` | A number you raise by hand when you change the profile. Every worker generation records the revision it started under, so the record shows which definition ran. Peen does not check it. |

A profile named `default` always exists with no Docker access, the `bridge` network, and no sudo. Defining a profile named `default` replaces the built-in one, which is how you give the default agent space an image or a mount. Defining a key Peen does not know, such as the removed `allowNetwork`, stops startup.

`docker: full` mounts the socket the controller itself uses, the one [the Docker socket rule](#the-docker-socket-rule) found. The Docker daemon resolves that path on the host, so a controller in a container must mount the socket at its literal host path. Peen grants the socket's owning group to the agent's account, so the agent reaches the daemon without running as root. A socket owned by group 0 cannot be granted that way, and Peen refuses to start while any profile sets `docker: full` beside one.

Three grants are risky and Peen says so. `docker: full` is host root, `network: host` removes the network boundary, and `sudo: true` gives the agent root inside its container. `GET /v1/agent-spaces` reports each Docker space with its grants, a `hostRootEquivalent` flag, and one sentence per risky grant in `capabilityWarnings`. The control surface shows the grants next to each name, for example `ops (docker: none, network: host, sudo: no)` and `builder (docker: full = host root, network: bridge, sudo: yes)`. You see them in the dropdown where you open a workspace and in the form that changes a session's agent space.

Peen reads `PEEN_DOCKER_PROFILES` at startup, so a profile change needs a controller restart. A restart stops every worker, and each session's next turn starts a worker under the new definition.

## Choosing an agent space for a workspace

Open a workspace in the control surface and pick an agent space in the "Agent space" dropdown, or leave it on "Controller default". Over the API, send `agentSpace` to `POST /v1/sessions/open`. It sets the agent space only when the call creates the session, so opening an existing workspace never moves it. Peen still checks the name on every open. A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED` and creates nothing, even for a workspace that already has a session.

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

The refusal reads:

```text
PEEN_DEFAULT_AGENT_SPACE=control: this controller can reach the Docker daemon at /var/run/docker.sock, and a reachable daemon makes control sessions host root, so use Docker profiles instead, for example PEEN_DEFAULT_AGENT_SPACE=docker:default, or run the controller without the socket
```

With a daemon in reach, the Docker agent spaces are the only ones offered, so Peen refuses to start when it cannot build the Docker worker launcher. The error names `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`, because the usual cause is a controller run with a numeric `--user uid:gid` that has no account in the image. Peen launches workers only through a local socket, so a daemon reachable only over `tcp://` also stops startup.

## Why startup was refused

Startup names the variable and the value it rejected. Besides the socket rule above, Peen refuses to start for:

- two Docker profiles with one name;
- a profile name over 64 characters, one that does not start with a letter or digit, or one with a character other than letters, digits, dots, underscores, and hyphens;
- a mount `source` or `target` that is not an absolute path;
- a negative `revision`;
- a key Peen does not know, or a `docker` or `network` value outside the lists above;
- `docker: readonly`;
- `docker: full` while the controller's socket is owned by group 0;
- anything after the JSON array in `PEEN_DOCKER_PROFILES`;
- a `PEEN_DEFAULT_AGENT_SPACE` that is not `control` or `docker:<profile>`, or that names a profile you did not define.

A default of `docker:<profile>` on a controller with no Docker daemon in reach passes startup, and every new session then fails at its first turn.

## Questions this page answers

- Where does my agent run? In the agent space of its workspace: `control` next to Peen, or `docker:<profile>` in its own container. The session list and `GET /v1/session` report it as `agentSpace`.
- What can it touch? See [Agent spaces](#agent-spaces) for `control`, and the profile's `mounts`, `docker`, `network`, and `sudo` for a Docker space.
- How do I give workspaces their own containers? Let the controller reach a Docker socket. Then every workspace runs in a `docker:<profile>` container, `docker:default` unless you pick another. You cannot keep some workspaces on `control`, because a reachable daemon removes it for all of them.
- How do I give an agent Docker access? Set `docker: full` in its profile. It is host root. There is no safer level yet.
- Why was my setup refused? See [why startup was refused](#why-startup-was-refused).
