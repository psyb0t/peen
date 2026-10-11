# Configuration

The installer writes `~/.config/peen/.env` for you, and `peen config edit` opens it. Without the installer, start from [`.env.example`](../.env.example) at the top of the repository, or let the Peen image write the same commented file for your directories with `config init`, as [deployment](deployment.md#durable-state-and-mounts) shows. `config init` never overwrites an existing file. Then set the provider values that fit your machine. Peen checks every `PEEN_` value before opening its listener, so a bad setting fails at startup instead of halfway through a task. Peen starts with no sessions. A client opens a workspace through `POST /v1/sessions/open`, and `PEEN_WORKSPACE_ROOT` bounds which directories it may name.

For Docker, `.env` is input for `docker run --env-file`. Do not source it from Bash, because `PEEN_UPSTREAMS` is raw JSON. For a bare binary, set the same values through your shell, service manager, or secret manager. This page is the full reference, and `.env.example` is the commented starting point.

`LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE` control application logging. Peen adds a JSON audit log configured by `PEEN_LOG_DIRECTORY` and `PEEN_LOG_RETENTION`.

| Variable | Default | Values |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `LOG_FORMAT` | `text` | `text` or `json`. `.env.example` sets `json`. |
| `LOG_ADD_SOURCE` | `false` | `true` adds the source file and line to each record. |

## Start here

These values decide where Peen keeps its state and which model handles a task. Start Peen from the directory that holds your projects, the workspace root, or set `PEEN_WORKSPACE_ROOT`. Docker users set that directory with `docker run --workdir`, and must use a literal host path when they want Docker agent spaces. Bare-process users change directory before launching Peen.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_CONFIG_DIR` | required, absolute | Global configuration and harness layer. Every Docker worker receives it read-only. A `control` worker can write it. See [directories](#directories). |
| `PEEN_STATE_DIR` | required, absolute | The controller's own durable state: SQLite, audit logs, worker sockets. No Docker worker receives it. A `control` worker can read it. See [directories](#directories). |
| `PEEN_WORKSPACE_ROOT` | process working directory | Absolute directory a client may open workspaces inside. See [workspace root](#workspace-root). |
| `PEEN_DOCKER_PROFILES` | empty, so only the built-in `default` profile | JSON array of named Docker profiles. Each one is the agent space `docker:<name>`. See [where it runs](where-it-runs.md#docker-profiles). |
| `PEEN_DEFAULT_AGENT_SPACE` | `docker:default` when the controller can reach a Docker socket, else `control` | Agent space a workspace opened without naming one uses: `control` or `docker:<profile>`. Startup fails when it is `control` and the controller can reach a Docker socket. See [the Docker socket rule](where-it-runs.md#the-docker-socket-rule). |
| `PEEN_WORKER_SOCKET_DIR` | `PEEN_STATE_DIR/workers` | The directory holding one directory per session worker. A worker receives only its own. |
| `PEEN_DOCKER_SOCKET` | empty | An extra Docker socket path to check. Peen also checks a `unix://` `DOCKER_HOST` and `/var/run/docker.sock`, and creates worker containers through the first socket where the Docker API answers. It refuses the `control` agent space when any of them, or a `tcp://` `DOCKER_HOST`, answers. See [the Docker socket rule](where-it-runs.md#the-docker-socket-rule). |
| `PEEN_WORKER_IMAGE` | empty | Image for every Docker worker, replacing each profile's `image`. Empty uses each profile's own `image`, and a profile without one runs the image published alongside this build. The installer sets it to the release it installed. Set it yourself to run workers from a local build. |
| `PEEN_HOST_USERNAME` | empty | Host account name for a Docker controller started with numeric `--user` IDs. Set it together with `PEEN_HOST_HOME`. The installer sets both. |
| `PEEN_HOST_HOME` | empty | Absolute host home for a Docker controller started with numeric `--user` IDs. Set it together with `PEEN_HOST_USERNAME`. |
| `PEEN_AGENT` | `core:general-purpose` | The agent every root turn runs as. A plain name picks a custom agent first and a core agent second, and `core:<name>` picks only the core one. Startup fails when a `core:` name is unknown or disabled. A plain name is resolved at each turn. See [Agents](agents.md). |
| `PEEN_DISABLED_CORE_SKILLS` | empty | Comma-separated core skills to remove, without the `core:` prefix, or `all`. The core skills are `delegation`, `freshness`, `plan-and-delegate`, and `planning`. An unknown name stops startup. See [core and custom skills and agents](harness.md#core-and-custom-skills-and-agents). |
| `PEEN_DISABLED_CORE_AGENTS` | empty | Comma-separated core agents to remove, without the `core:` prefix, or `all`. The only core agent is `general-purpose`, and it is the default `PEEN_AGENT`. To disable it, first set `PEEN_AGENT` to one of your own agents. Startup fails on an unknown name, and when `PEEN_AGENT` names a disabled `core:` agent. A plain `PEEN_AGENT` name passes startup, and a turn fails when the workspace defines no agent with that name. |
| `PEEN_UPSTREAMS` | required, JSON | Named provider list. See [providers](#providers). |
| `PEEN_DEFAULT_MODEL` | required | `provider/model` for the root agent and, unless `PEEN_COMPACTION_MODEL` is set, compaction. |
| `PEEN_COMPACTION_MODEL` | `PEEN_DEFAULT_MODEL` | `provider/model` used only for the summarization call. See [compaction](#compaction). |
| `PEEN_HTTP_LISTEN_ADDRESS` | `:8080` | Listener address. `:8080` listens on every interface. The installer writes `127.0.0.1:8080`, which only this machine can reach. |
| `PEEN_API_TOKEN` | empty | Bearer token. Empty turns authentication off. |
| `PEEN_METRICS_LISTEN_ADDRESS` | `127.0.0.1:9090` | Separate loopback-only Prometheus listener. See [metrics](#metrics). |

## Settings the peen command reads

The installer's `peen` command keeps three settings of its own in `.env`. The controller ignores them, and a `control` worker never receives them.

| Variable | Set by | Meaning |
| --- | --- | --- |
| `PEEN_CONTROL_SPACE` | `peen setup --control-space` | Where Peen runs: `host` for the binary in `~/.local/lib/peen`, or `/usr/local/lib/peen` after an install with `--system`, or `docker` for a container. `peen config set` refuses it, because changing it needs `peen setup` to fetch the binary or the image. |
| `PEEN_IMAGE` | `peen setup`, `peen upgrade` | The release image this install runs, such as `psyb0t/peen:v0.20.0`. The `peen` command runs the controller container from it, takes the host binary out of it, and moves `PEEN_WORKER_IMAGE` along with it while the two still match. |
| `PEEN_CONTAINER_NAME` | `peen setup --container-name` | The controller container's name in the docker control space. The default is `peen-<your user name>`. |

The `peen` command also reads `PEEN_HTTP_LISTEN_ADDRESS` as the address Peen is reachable at on this machine. In the docker control space it publishes that address and runs the controller on `:8080` inside the container. [Deployment](deployment.md#install) describes the command.

## Directories

Peen keeps two directories apart, and refuses to start if they overlap.

`PEEN_CONFIG_DIR` is the global configuration and harness layer: `AGENTS.md`, `.agents/`, and the optional `SYSTEM.md`, `APPEND_SYSTEM.md`, and `COMPACTION.md`. Every Docker worker receives this directory read-only, so anything inside it is readable by every session. A `control` worker runs as the controller's user and can write it.

`PEEN_STATE_DIR` is the controller's own durable state: `peen.db`, the audit logs, and the worker socket directories. No Docker worker receives it. A `control` worker runs as the controller's user and can read it, every session's transcript included. See [where it runs](where-it-runs.md#agent-spaces).

Peen refuses to start when the two are the same path, or when either sits inside the other. Without that rule, the read-only configuration mount would carry SQLite into every worker, and one session could read every other session's transcript.

The check resolves both paths through their symlinks before comparing, so a state directory that is a link into the configuration directory is refused even though the two strings share no prefix. A directory Peen has not created yet is normal, so Peen resolves the deepest part that exists and adds the rest of the path back. A path that cannot be resolved for any other reason stops startup.

```bash
PEEN_CONFIG_DIR=/absolute/path/to/peen/config
PEEN_STATE_DIR=/absolute/path/to/peen/state
```

Siblings under a shared parent are fine. Nesting is not.

## Workspace root

The controller has one workspace root. Each chat opens a workspace, which is the root itself or any existing directory inside it, and every workspace gets its own agent session. Point the root at the directory that holds your projects:

```bash
PEEN_WORKSPACE_ROOT=/srv/work
```

With that root, a client can open `/srv/work/api` and `/srv/work/web` as two separate workspaces. Peen resolves the requested path through its symlinks before checking it, so an alias of an allowed directory opens the same session as the real path, and a symlink inside the root that points outside it is refused. A path outside the root returns `403 WORKSPACE_NOT_ALLOWED` and creates no session.

Peen also refuses a workspace that is, contains, or sits inside `PEEN_STATE_DIR`, and one that is or contains `PEEN_CONFIG_DIR`, with the same `403 WORKSPACE_NOT_ALLOWED`. With a root of `$HOME` and state under `~/.config/peen`, `~` itself cannot be opened, and the project directories inside it can. See [where it runs](where-it-runs.md#workspaces-that-would-expose-peens-own-directories).

Leaving the variable unset uses the process working directory as the root. The `docker run` commands in [deployment](deployment.md#docker) set the working directory with `-w` to the same directory. The installer sets `PEEN_WORKSPACE_ROOT` to your home directory or to `--workspace-root`.

Peen refuses to start when the root is relative or missing, so a typo fails at startup rather than when a client first opens a workspace.

## Agent spaces

A session's agent runs in an agent space: `control`, which runs it where Peen runs, or `docker:<profile>`, which runs it in its own container. A client names an agent space when it opens a workspace and never sends an image, mount, network setting, or capability. [Where it runs](where-it-runs.md) is the reference for both kinds, for every Docker profile option and its default, and for the rule that refuses `control` when the controller can reach a Docker socket. The settings are `PEEN_DOCKER_PROFILES` and `PEEN_DEFAULT_AGENT_SPACE` in the table above.

Naming an agent space the deployment does not offer returns 403 and creates nothing, even when the workspace already has a session. Opening an existing session never changes the agent space it already runs in. Moving a session is `POST /v1/session/reconfigure`. See [choosing an agent space](where-it-runs.md#choosing-an-agent-space-for-a-workspace).

## Docker workers

A `docker:<profile>` agent space runs the session worker in a sibling container named `peen-worker-<session-uuid>`. The `control` agent space runs it as a child process of the controller, using the same Peen binary.

Peen picks the worker image in this order: `PEEN_WORKER_IMAGE` if it is set, then the Docker profile's own `image`, then the image published alongside the running controller. That last one is `psyb0t/peen` tagged with the controller's version, which is the tag the release was published under, so a release runs workers from its own image with no setting at all.

A build from source reports the version `dev`, and no registry holds `psyb0t/peen:dev`. Set `PEEN_WORKER_IMAGE` to run Docker workers from a local build, such as the `peen` image `make docker-build` creates.

Peen accepts any reference the daemon can resolve, by tag or by digest, from any repository. The image must contain the same Peen release as the controller, because the controller and its workers talk a protocol that changes between releases, so build a custom worker image `FROM psyb0t/peen:<the controller's version>`. A worker container starts as root so the Peen entrypoint can create the controller's host account and drop to it, so an image without that entrypoint fails when the worker starts.

Peen pulls an image the daemon does not hold yet. Creating a container through the Docker API does not pull the way the `docker` CLI does, so without this a worker could not start on a host that had never seen the image.

Peen records the repository digest the daemon reports for the image with each worker it starts, so naming a tag still leaves a record of the exact image that ran. A locally built image has no repository digest until it is pushed, and Peen then records none rather than the local image ID, which no registry could resolve.

A Docker agent space needs the controller to reach a Docker socket. Peen decides that at startup, as [the Docker socket rule](where-it-runs.md#the-docker-socket-rule) describes. Without one, a session in a Docker agent space is refused. Peen never falls back to the `control` agent space, because that would run the model's tools next to Peen after the operator asked for a container. With one, Peen refuses to start when it cannot prepare to start worker containers, because the Docker agent spaces are then the only ones offered.

A Docker worker runs as the controller's own host UID, GID, and username, so files it writes in a mounted workspace keep host ownership. Peen refuses to start a Docker worker as root. The workspace is mounted writable at its literal host path, the configuration directory read-only at its literal path, and the session's own directory writable, so a path in a transcript means the same thing inside and outside the container.

A controller on the host reads its username and home from the operating system. A controller running in Docker with `--user uid:gid` has no account entry for that host user, so set `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` together. Peen uses those values with the controller's numeric UID and GID to recreate the same account inside each worker. Setting only one of them, or a relative home, fails startup.

A Docker worker receives only the runtime configuration it needs to run a turn: the provider definitions, model selection, limits, and the provider credentials named by `apiKeyEnv`. It never receives `PEEN_STATE_DIR`, `PEEN_API_TOKEN`, the controller's Docker access, or the entrypoint's identity settings. Commands the agent runs, in any agent space, start with an environment that has `PEEN_API_TOKEN` and every `apiKeyEnv` variable removed. They still run as the same user as the worker process that holds the provider credential. The worker is non-dumpable on Linux, so they cannot read the credential from its `/proc` files, but an agent with root, as `sudo: true` gives, may reach it. Do not give a Docker profile a workspace containing secrets you would not expose to it. A hook `command` action gets only `PATH` and the variables its `environment` declares.

### Sudo

`sudo: true` in a Docker profile gives the agent working sudo inside its own container. The image installs sudo but ships no sudoers rule, so sudo authorizes nobody until a profile asks for it.

Every Docker worker starts as root only to set up its account. The entrypoint creates or updates the controller's host UID, GID, and username inside the container, then drops to that account with `setpriv` before the agent starts. This keeps host file ownership and the host username correct inside a worker.

For a `sudo: true` profile, the entrypoint also writes `/etc/sudoers.d/peen-worker` for that account. Other Docker profiles write no sudoers rule and run with Docker's `no-new-privileges` guard. The `control` agent space has no `sudo` option, because it has no entrypoint to grant anything.

This is root inside the container. Combined with `mounts`, it reaches whatever those mounts expose. It is a separate decision from `docker: full`, and neither implies the other.

### Docker access

`docker: full` is separate from the controller's own Docker access. The controller's socket lets it create worker containers. This option also gives the worker its own access to the daemon, which is host root. Peen mounts the controller's socket at `/var/run/docker.sock` in the worker and adds the socket's owning group, read from the socket itself, to the worker's groups, so the worker reaches the daemon without running as root. Peen refuses to start while a profile sets `docker: full` and the socket is owned by group 0. See [Docker profiles](where-it-runs.md#docker-profiles).

`docker: readonly` gives the worker no socket. The controller starts a proxy for that worker at `docker.sock` in the worker's own session directory, forwards only read requests to its own socket, and sets `DOCKER_HOST` in the worker to the proxy. The proxy stops with the worker. [`docker: readonly`](where-it-runs.md#docker-readonly) lists what it answers and what it refuses.

### Worker sockets

`PEEN_WORKER_SOCKET_DIR` defaults to `PEEN_STATE_DIR/workers`. Each session gets its own directory inside it, `<directory>/<session-uuid>/worker.sock`, and a Docker worker gets that one session directory. It never gets the parent, because the parent holds every live session's socket.

A Unix socket path holds at most 107 bytes, and the directory plus the session directory and file name has to fit. Peen measures this at startup and refuses to start with the length it computed. A deep state directory is fixed by setting a shorter `PEEN_WORKER_SOCKET_DIR`.

## Logging and audit trail

`LOG_LEVEL` controls which structured application records go to stdout. The audit log keeps every record at debug level and above in one file per UTC day, `PEEN_LOG_DIRECTORY/YYYYMMDD-000000.log`. When `PEEN_LOG_DIRECTORY` is empty, the directory is `PEEN_STATE_DIR/logs`. Set a path to move it, and keep it out of `PEEN_CONFIG_DIR`, which every worker can read.

`PEEN_LOG_RETENTION` is a Go duration, default `336h`, and keeps that many days of daily files, so `336h` keeps at most 14 files. Go durations have no day unit, so write days as hours, such as `168h` for 7 days and `720h` for 30 days. The value must be a whole number of days, a multiple of `24h`, because files are per UTC day. `0` means the default. Startup fails on a negative value or one that is not a multiple of `24h`. Peen creates the directory with mode `0700` and the files with `0600`.

[ctxscope](https://github.com/psyb0t/ctxscope) carries request, session, turn, child-agent, model, tool-call, and service fields through every record. Audit records name the resolved harness files, skill activation, hook actions, child-agent lifecycle, and provider and tool outcomes, plus content byte counts and SHA-256 digests.

The agent loop runs in a session's worker, so those records come from another process. The controller reads each worker's output and writes it through its own logging at the level the worker used, tagged with `session_id` and `worker_generation_id`. That is what puts them in this file. A worker receives neither the audit directory nor the state directory, so it never writes here itself and never sees another session's records.

Raw user prompts, model thinking, tool arguments, tool results, environment values, and credentials are not copied into logs. Hook records may include small operational counters such as the estimated size of the active context. The SQLite transcript keeps the full, verbatim record for authorized debugging, including model requests, responses, usage, costs, tool definitions, child-agent runs, and compactions. SQL statement previews are turned off, because an expanded statement can contain stored sensitive content.

## Providers

`PEEN_UPSTREAMS` is a JSON array with one entry per provider. The docs call an entry an upstream or a provider, and the API calls its name `connectionName`. See [Terms](architecture.md#terms).

```bash
PEEN_UPSTREAMS=[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
AIGATE_TOKEN=your-token-here
```

A Z.ai Coding entry, as `.env.example` has it, looks like this:

```bash
PEEN_UPSTREAMS=[{"name":"zai","type":"zai-coding","baseUrl":"https://api.z.ai/api/coding/paas/v4","apiKeyEnv":"ZAI_TOKEN"}]
ZAI_TOKEN=your-token-here
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Local name, the prefix in `name/model`, such as `aigate/your-model-id`. No `/`. Unique across entries. |
| `type` | yes | Wire protocol: `openai`, `anthropic`, or `zai-coding`. |
| `baseUrl` | no | API base URL. Left out, the protocol's own default endpoint is used. |
| `apiKeyEnv` | no | Name of the environment variable that holds the key. Never put the key itself here. If set, that variable must be set and non-empty, or Peen refuses to start. |

Peen asks every listed provider for its models at startup. Delete every entry you do not use, together with its key variable. An entry whose key variable is missing or empty stops Peen from starting. An entry with a placeholder key such as `REPLACE_ME` fails discovery. Peen logs a warning and offers none of its models, and startup fails if `PEEN_DEFAULT_MODEL` or `PEEN_COMPACTION_MODEL` names one of them.

## Model selection

`PEEN_UPSTREAMS` accepts `openai`, `anthropic`, and `zai-coding` provider types. `.env.example` has an OpenAI-compatible [AIGate](https://github.com/psyb0t/aigate) entry and Z.ai's Coding endpoint. `zai-coding` keeps Z.ai thinking state through tool rounds and applies its model-specific thinking controls through [Elelem](https://github.com/psyb0t/elelem).

Set `PEEN_DEFAULT_MODEL` to the model that runs every turn and `PEEN_COMPACTION_MODEL` to the one that writes [compaction](#compaction) summaries, each as `provider/model`. With the Z.ai entry above, that is `PEEN_DEFAULT_MODEL=zai/glm-5.3` and `PEEN_COMPACTION_MODEL=zai/glm-5.3-flash`. A client can pick another model for one message. Peen never picks a model for a request on its own.

Peen's Z.ai Coding catalog has GLM 5.3 and GLM 5.3 Flash. GLM 5.3 supports its documented reasoning effort values. GLM 5.3 Flash keeps its thinking between rounds and has no numeric or off setting for it.

## Metrics

`GET /metrics` serves Peen's [Prometheus](https://github.com/prometheus/client_golang) metrics on `PEEN_METRICS_LISTEN_ADDRESS`. It never appears on the API listener, is not part of `/v1`, and does not check `PEEN_API_TOKEN`, because the loopback-only listener is the access boundary.

The value must be a loopback IP address and a port other than 0, such as `127.0.0.1:9090` or `[::1]:9090`. Peen refuses to start with a wildcard or non-loopback address. Run a scraper in the same network namespace as Peen. A Docker `-p` rule does not make a listener on the container's loopback interface reachable from the host.

## Context and turn limits

Every time setting is a Go duration such as `90s`, `10m`, or `1h30m`. A bare number such as `120` and an empty value are rejected at startup. `PEEN_TURN_TIMEOUT` and `PEEN_COMPACTION_TIMEOUT` must be positive. For `PEEN_TOOL_TIMEOUT`, `PEEN_TOOL_COMMAND_TIMEOUT`, `PEEN_TOOL_MAX_COMMAND_TIMEOUT`, and `PEEN_HOOK_COMMAND_TIMEOUT`, `0` means the default and a negative value fails startup. For `PEEN_LOG_RETENTION`, `0` means the default, as [logging](#logging-and-audit-trail) explains.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CONTEXT_TOKENS` | `32768` | The request budget [Elelem](https://github.com/psyb0t/elelem) enforces. It is also the context size for a model whose provider publishes none, and startup rejects a value larger than a published window. |
| `PEEN_COMPACTION_MODE` | `drop-oldest` | `drop-oldest` or `summarize`. See [compaction](#compaction). |
| `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` | `2048` | Space kept for a generated summary. Must be smaller than `PEEN_MAX_CONTEXT_TOKENS`, and is checked even when `drop-oldest` is active. |
| `PEEN_COMPACTION_TIMEOUT` | `2m` | Limit on the separate summarization call. |
| `PEEN_TURN_TIMEOUT` | `10m` | Limit on one turn. |
| `PEEN_MAX_CONCURRENT_TURNS` | `16` | Turns that may run at once, across every session in the process. |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `16` | Messages sent while a turn is running wait until the model's current round ends. At most this many can wait at once, counting each skill message or already-loaded note that follows a queued message. A long turn can accept more in total, because delivery frees slots. Each one is reported with `userMessage.queued` and then `userMessage.delivered`. See [queued messages](http-api.md#send-and-watch-turns-over-websocket). |

### Compaction

A conversation grows until a request no longer fits in `PEEN_MAX_CONTEXT_TOKENS`. Compaction is what Peen does then, and `PEEN_COMPACTION_MODE` picks how:

- `drop-oldest` leaves whole old conversation units out of the request and stores nothing. The transcript keeps every message.
- `summarize` replaces an old completed part of the conversation with a summary made by `PEEN_COMPACTION_MODEL`, stores the summary, and never deletes transcript rows. `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` keeps room for the summary, and `PEEN_COMPACTION_TIMEOUT` limits the call. An optional `COMPACTION.md` in `PEEN_CONFIG_DIR` replaces the built-in summarizer instructions. Only this mode runs the `preCompact` and `postCompact` [hooks](hooks.md).

Under `summarize` a child agent compacts its own transcript into its own stored summaries, separate from the session's. `GET /v1/session/compactions` and `GET /v1/session/agents/{agentRunId}/compactions` read them. Peen checks the summary settings in both modes, so switching modes cannot expose a setting startup already accepted.

## Message size bounds

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_MESSAGE_BYTES` | `262144` | Limits the `message` field in a `message.send` WebSocket frame. |
| `PEEN_MAX_SYSTEM_PROMPT_BYTES` | `65536` | Limits the optional [`systemPrompt.content`](http-api.md#send-and-watch-turns-over-websocket) field of a `message.send` frame. |
| `PEEN_MAX_STORED_MESSAGE_BYTES` | `1048576` | Limits any stored message of any role, since an assistant or tool message has no caller-facing limit of its own. |

## Tool execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_TOOL_ROUNDS` | `32` | Tool-call rounds allowed in one turn. |
| `PEEN_MAX_CONCURRENT_TOOLS` | `4` | Tool calls one turn may run at once. |
| `PEEN_TOOL_TIMEOUT` | `15m` | Limit on one tool call, including a `runCommand` wait. |
| `PEEN_MAX_TOOL_RESULT_TOKENS` | `8192` | Limits a tool result before it goes back to the model. |
| `PEEN_TOOL_MAX_LIST_ENTRIES` | `1000` | `listFiles` entry limit. |
| `PEEN_TOOL_MAX_LIST_DEPTH` | `16` | `listFiles` recursion depth limit. |
| `PEEN_TOOL_MAX_SEARCH_MATCHES` | `200` | `searchText` match limit. |
| `PEEN_TOOL_MAX_SEARCH_FILE_BYTES` | `2097152` | Largest file `searchText` will scan. |
| `PEEN_TOOL_MAX_READ_BYTES` | `262144` | `readFile` byte limit per call. |
| `PEEN_TOOL_MAX_READ_LINES` | `2000` | `readFile` line limit per call. |
| `PEEN_TOOL_MAX_WRITE_BYTES` | `4194304` | `writeFile` size limit. |
| `PEEN_TOOL_MAX_EDITS` | `64` | Replacements allowed in one `editFile` call. |
| `PEEN_TOOL_MAX_DIFF_BYTES` | `65536` | Size limit on the diff `editFile` returns. |
| `PEEN_TOOL_MAX_REMOVE_ENTRIES` | `20000` | `removePath` recursive entry limit. |
| `PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES` | `65536` | `runCommand` captured stdout and stderr limit. |
| `PEEN_TOOL_COMMAND_TIMEOUT` | `2m` | Default `runCommand` wait. The command is never killed when this elapses. A command still running returns a job handle instead. |
| `PEEN_TOOL_MAX_COMMAND_TIMEOUT` | `15m` | Longest wait a call may request. Must not exceed `PEEN_TOOL_TIMEOUT`. |

## Hook execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_HOOK_COMMAND_TIMEOUT` | `30s` | Limit on one `command` hook action. Must not exceed `PEEN_TOOL_TIMEOUT`. |
| `PEEN_MAX_HOOK_COMMAND_OUTPUT` | `65536` | Maximum stdout or stderr captured from one hook command. |

See [hooks](hooks.md) for the file format, matching rules, event order, and how actions run.

## Session notices

Background jobs, child agents, hooks, and outside callers report what happened through session notices, also called [session events](events.md). Peen hands them to the model at the next turn or tool boundary, or starts a turn when an event handler allows a wake.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_PENDING_EVENTS` | `256` | Per-session limit on the in-memory copy of pending notices. The oldest copy is dropped and counted in metrics. Delivery to the model reads stored notices from SQLite, so this limit never drops what the model receives. |
| `PEEN_MAX_EVENT_SUMMARY_BYTES` | `4096` | Largest `summary` on a notice. `POST /v1/session/notices` rejects a longer one with `400 VALIDATION_FAILED`, and a hook that emits one fails its action. Peen shortens its own `job.*` and `agent.*` summaries to fit, without splitting a character. |
| `PEEN_MAX_EVENT_DATA_BYTES` | `65536` | Largest encoded `data` on a notice. `POST /v1/session/notices` rejects larger data with `400 VALIDATION_FAILED`, and a hook that emits it fails its action. Peen leaves over-size data out of its own `job.*` and `agent.*` events. |
| `PEEN_MAX_EVENT_WAKES_PER_HOUR` | `60` | Per-session limit on turns started by a wake. An event over the limit stays queued for the next turn or tool boundary. `0` removes the limit. Peen keeps the count in memory, so it starts over when Peen restarts. |

See [session events](events.md) for event types, delivery, wakes, and event handlers.

## Child agent limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `5` | How many `launchAgent` calls may nest. A child at that depth does not receive `launchAgent`. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child conversation may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Agent runs one session may have in flight at once. |
| `PEEN_MAX_AGENT_RUN_EVENT_COUNT` | `2000` | How many of a running child's recent events Peen keeps in memory for clients following it live. SQLite keeps every event regardless. |
| `PEEN_MAX_AGENT_RUN_EVENT_BYTES` | `65536` | Byte limit on that same in-memory list. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size limit on an inline `agentDefinition.instructions` string. |
| `PEEN_ASK_TIMEOUT` | `0` | How long the `ask` tool waits for an answer, as a Go duration such as `30m`. A call may ask for a shorter wait. `0` sets no limit of its own, so `ask` waits up to the 30-day cap every messaging tool has, and a child whose question nobody answers holds one of the `PEEN_MAX_CONCURRENT_AGENT_RUNS` slots until someone answers or stops it. The root's `ask` still ends with its turn, at `PEEN_TURN_TIMEOUT`. A negative value stops startup. |

A child's inbox holds at most `PEEN_MAX_QUEUED_USER_MESSAGES` messages that it has not read yet, the same bound as the root's queue. See [running children](agents.md#running-children).

## Harness layering

Rules, skills, named agents, event handlers, and hooks are files Peen reads from the configuration directory and from every directory between `/` and the workspace, fresh at the start of every turn. [The harness guide](harness.md) covers the layer order, how layers combine, what the model sees, and the limits. Each piece has its own page: [rules](rules.md), [skills](skills.md), [named agents](agents.md), [session events and event handlers](events.md), and [hooks](hooks.md).

Every root and child turn also receives the server's current local time, timezone, operating system, CPU architecture, logical CPU count, and Go runtime as trusted context. The built-in `freshness` skill tells the model to check local project facts and verify outside facts that may have changed.

## Process jobs

`runCommand` never kills a process because a wait ran out. A command that exits within `PEEN_TOOL_COMMAND_TIMEOUT`, or a caller-requested `timeout` up to `PEEN_TOOL_MAX_COMMAND_TIMEOUT`, returns its exit status and output normally. A command still running when the wait ends keeps running, and the tool returns a job handle plus the output captured so far. The `background` argument skips the wait and returns the handle at once.

`runCommand` and `waitJob` take `timeout` as a Go duration string such as `"90s"`, `"5m"`, or `"1h30m"`. Leaving it out uses the default, and `0s`, negative values, and bare numbers are rejected. For `runCommand` the default is `PEEN_TOOL_COMMAND_TIMEOUT`, and a value above `PEEN_TOOL_MAX_COMMAND_TIMEOUT` is lowered to that maximum rather than rejected. For `waitJob` the default is `30s`, and Peen lowers any value above `10m` to `10m`. No `PEEN_` variable changes these two `waitJob` limits.

Jobs belong to the session, not the turn. A command started in one turn stays listable, readable, and signalable from a later turn, and cancelling the turn that started it does not stop it. Peen writes every stdout and stderr line to SQLite before it enters a small in-memory buffer, which only the active tool call reads. REST output replay, job details, and signal history come from SQLite and survive reconnects and restarts. On shutdown, Peen stops every running job, waits a grace period, then kills its process group, so no job outlives Peen. A job that finishes after its `runCommand` call returned publishes a `job.exited`, `job.signalled`, or `job.failed` [session event](events.md).

## Agent run observability

Each `launchAgent` call creates a SQLite run record and stored event records. Peen updates the record when a cancel is requested and when the run ends. Use the session agent-run REST endpoints to inspect or cancel a run, list the runs waiting on a question, and read a run's events during and after it. SQLite is the only stored replay record. Peen keeps no JSONL copy of transcripts.
