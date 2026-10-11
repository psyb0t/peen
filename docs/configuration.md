# Configuration

Copy `.env.example` to `.env`, then set the provider values that fit your
machine. Peen validates every `PEEN_` value before opening its listener, so a
bad setting fails at startup instead of halfway through a task. Peen starts
with no sessions. A client opens a workspace through `POST /v1/sessions/open`,
and `PEEN_WORKSPACE_ROOT` bounds which directories it may name.

For Docker, `.env` is input for `docker run --env-file`. Do not source it from
Bash because `PEEN_UPSTREAMS` is raw JSON. For a bare binary, set the same
values through your shell, service manager, or secret manager. This page is
the full reference; the example file is the commented starting point.

`LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE` control application logging.
Peen adds a JSON audit sink configured by `PEEN_LOG_DIRECTORY` and
`PEEN_LOG_RETENTION`.

| Variable | Default | Values |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `LOG_FORMAT` | `text` | `text` or `json`. `.env.example` sets `json`. |
| `LOG_ADD_SOURCE` | `false` | `true` adds the source file and line to each record. |

## Start here

These values decide where Peen keeps its state and which model handles a task.
Start Peen from the directory that holds your projects (the workspace root), or
set `PEEN_WORKSPACE_ROOT`. Docker users set that directory with
`docker run --workdir` and must use a literal host path when they want Docker
agent spaces. Bare-process users change directory before launching Peen.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_CONFIG_DIR` | required, absolute | Global configuration and harness layer. Every Docker worker receives it read-only. A `control` worker can write it. See [directories](#directories). |
| `PEEN_STATE_DIR` | required, absolute | Controller-owned durable state: SQLite, audit logs, worker sockets. No Docker worker receives it. A `control` worker can read it. See [directories](#directories). |
| `PEEN_WORKSPACE_ROOT` | process working directory | Absolute directory a client may open workspaces inside. See [workspace root](#workspace-root). |
| `PEEN_DOCKER_PROFILES` | empty, so only the built-in `default` profile | JSON array of named Docker profiles. Each one is the agent space `docker:<name>`. See [where it runs](where-it-runs.md#docker-profiles). |
| `PEEN_DEFAULT_AGENT_SPACE` | `docker:default` when the controller can reach a Docker socket, else `control` | Agent space a workspace opened without naming one uses: `control` or `docker:<profile>`. Startup fails when it is `control` and the controller can reach a Docker socket. See [the Docker socket rule](where-it-runs.md#the-docker-socket-rule). |
| `PEEN_WORKER_SOCKET_DIR` | `PEEN_STATE_DIR/workers` | Root holding one directory per session worker. A worker receives only its own. |
| `PEEN_DOCKER_SOCKET` | empty | An extra Docker socket path to check. Peen also checks a `unix://` `DOCKER_HOST` and `/var/run/docker.sock`, uses the first socket where the Docker API answers to create worker containers, and refuses the `control` agent space when any of them, or a `tcp://` `DOCKER_HOST`, answers. See [the Docker socket rule](where-it-runs.md#the-docker-socket-rule). |
| `PEEN_WORKER_IMAGE` | empty | Image for every Docker worker, replacing each profile's `image`. Empty uses each profile's own `image`, and a profile without one runs the image published alongside this build. Set it to run a worker from a local build. |
| `PEEN_HOST_USERNAME` | empty | Host account name for a Docker controller started with numeric `--user` IDs. Set together with `PEEN_HOST_HOME`. |
| `PEEN_HOST_HOME` | empty | Absolute host home for a Docker controller started with numeric `--user` IDs. Set together with `PEEN_HOST_USERNAME`. |
| `PEEN_AGENT` | `core:general-purpose` | Root agent. A plain name picks a custom agent first and a core agent second, and `core:<name>` picks only the core one. Startup fails when a `core:` name is unknown or disabled. Any plain name is resolved at each turn. See [Agents](agents.md). |
| `PEEN_DISABLED_CORE_SKILLS` | empty | Comma-separated core skills to remove, without the `core:` prefix, or `all`. The core skills are `freshness` and `planning`. An unknown name stops startup. See [core and custom skills and agents](harness.md#core-and-custom-skills-and-agents). |
| `PEEN_DISABLED_CORE_AGENTS` | empty | Comma-separated core agents to remove, without the `core:` prefix, or `all`. The only core agent is `general-purpose`. Startup fails on an unknown name, and when `PEEN_AGENT` is a `core:` name that is disabled, which includes the default `core:general-purpose`. A plain `PEEN_AGENT` name passes startup and fails at a turn whose workspace defines no such custom agent. To disable `general-purpose`, set `PEEN_AGENT` to a custom agent. |
| `PEEN_UPSTREAMS` | required, JSON | Named provider list. See [providers](#providers). |
| `PEEN_DEFAULT_MODEL` | required | Qualified `provider/model` for the root agent and, unless overridden, compaction. |
| `PEEN_COMPACTION_MODEL` | `PEEN_DEFAULT_MODEL` | Qualified `provider/model` used only for the summarization call. See [compaction](#compaction). |
| `PEEN_HTTP_LISTEN_ADDRESS` | `:8080` | Listener address. |
| `PEEN_API_TOKEN` | empty | Bearer token. Empty disables authentication. |
| `PEEN_METRICS_LISTEN_ADDRESS` | `127.0.0.1:9090` | Separate loopback-only Prometheus listener. See [metrics](#metrics). |

## Directories

Peen keeps two directories apart, and refuses to start if they overlap.

`PEEN_CONFIG_DIR` is the global configuration and harness layer: `AGENTS.md`, `.agents/`, and the optional `SYSTEM.md`, `APPEND_SYSTEM.md`, and `COMPACTION.md`. Every Docker worker receives this directory read-only, so anything inside it is readable by every session. A `control` worker runs as the controller's user and can write it.

`PEEN_STATE_DIR` is the controller's own durable state: `peen.db`, the audit logs, and the worker socket root. No Docker worker receives it. A `control` worker runs as the controller's user and can read it, every session's transcript included. See [where it runs](where-it-runs.md#agent-spaces).

Peen refuses to start when the two are the same path, or when either sits inside
the other. Without that rule, the read-only configuration mount would carry
SQLite into every worker, and one session could read every other session's
transcript.

The check resolves both paths through their symlinks before comparing, so a
state directory that is a link into the configuration directory is refused even
though the two strings share no prefix. A directory Peen has not created yet is
normal, so the deepest existing ancestor is resolved and the remaining names are
rejoined onto it. A path that cannot be resolved for any other reason refuses
startup rather than falling back to the literal string.

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

Leaving the variable unset uses the process working directory as the root. The Docker command in the README sets the working directory with `-w`, so there the mounted directory is the root.

Peen refuses to start when the root is relative or missing, so a typo fails at startup rather than when a client first opens a workspace.

## Agent spaces

A session's agent runs in an agent space: `control`, which runs it where Peen runs, or `docker:<profile>`, which runs it in its own container. A client names an agent space when it opens a workspace and never sends execution arguments. [Where it runs](where-it-runs.md) is the reference for both kinds, for every Docker profile option and its default, and for the rule that refuses `control` when the controller can reach a Docker socket. The settings are `PEEN_DOCKER_PROFILES` and `PEEN_DEFAULT_AGENT_SPACE` in the table above.

Naming an agent space the deployment does not offer returns 403 and creates nothing, even when the workspace already has a session. Opening an existing session never changes the agent space it already runs in. Moving a session is `POST /v1/session/reconfigure`. See [choosing an agent space](where-it-runs.md#choosing-an-agent-space-for-a-workspace).

## Docker workers

A `docker:<profile>` agent space runs the session worker in a sibling container named `peen-worker-<session-uuid>`. The `control` agent space runs it as a child process of the controller, using the same installed Peen binary.

Peen picks the worker image in one order: `PEEN_WORKER_IMAGE` if it is set, then
the Docker profile's own `image`, then the image published alongside the running
controller. That last one is `psyb0t/peen` tagged with the controller's own build
version, which is why a release runs the worker built beside it without anyone
editing configuration. A digest cannot do that, because the digest does not
exist until the push that creates it has finished.

A build with no release tag on `HEAD` reports `dev`, so it names an image the
registry has no reason to hold. Set `PEEN_WORKER_IMAGE` to run a Docker worker
from a local build.

Any reference the daemon can resolve is accepted, by tag or by digest, from any
repository. A deployment naming its own image is naming it on purpose, and a
controller that can create containers at all can already create them from any
image. A worker container does start as root so the Peen entrypoint can create
the controller's host account and drop to it, so an image without that
entrypoint fails when the worker runs, not before.

An image the daemon does not already hold is pulled. The Engine API's container
create does not pull the way the `docker` CLI does, so without this a worker
could not start on a host that had never seen the image.

Peen records the repository digest the daemon reports for the image on the worker
generation, so naming a tag still leaves a durable record of the exact bytes that
ran. A locally built image has no repository digest until it is pushed, and the
generation then records none rather than the local image ID, which no registry
could resolve.

A Docker agent space needs the controller to reach a Docker socket. Peen decides that at startup, as [the Docker socket rule](where-it-runs.md#the-docker-socket-rule) describes. Without one, a session in a Docker agent space is refused. Peen never falls back to the `control` agent space, because that would run the model's tools next to Peen after the operator asked for a container. With one, Peen refuses to start when it cannot build the Docker worker launcher, because the Docker agent spaces are then the only ones offered.

A Docker worker runs as the controller's own host UID, GID, and username, so
files it writes in a mounted workspace keep host ownership. Peen refuses to start
a Docker worker as root. The workspace is mounted writable at its literal host
path, the config directory read-only at its literal path, and the session's own
socket directory writable, so a path in a transcript means the same thing inside
and outside the container.

A controller on the host reads its username and home from the operating system. A
controller itself running in Docker with `--user uid:gid` has no passwd entry
for that host account, so set `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` together.
Peen uses those values with the controller's numeric UID and GID to recreate the
same account inside each worker. A partial pair or a relative home fails startup.

A Docker worker receives only the runtime configuration required to run a turn. This includes the provider definitions, model selection, limits, and the named provider credentials selected by `apiKeyEnv`. It never receives `PEEN_STATE_DIR`, the public `PEEN_API_TOKEN`, controller Docker authority, or the entrypoint's identity controls. Commands the agent runs, in any agent space, start with an environment that has `PEEN_API_TOKEN` and every `apiKeyEnv` variable removed. They still run as the same user as the worker process that holds the provider credential, so treat that credential as reachable by the agent, and do not give a Docker profile a workspace containing secrets you would not expose to it. A hook `command` action gets only `PATH` and the variables its `environment` declares.

### Sudo

`sudo: true` in a Docker profile gives the agent working sudo inside its own container.
The image installs sudo but ships no sudoers rule, so sudo authorizes nobody
until a profile asks for it.

Every Docker worker starts as root only for entrypoint bootstrap. The entrypoint
creates or reconciles the controller host UID, GID, and username inside the
image, then drops to that account with `setpriv` before the agent starts. This
keeps both host file ownership and the host username correct inside a worker.

For a `sudo: true` profile, the entrypoint also writes
`/etc/sudoers.d/peen-worker` for exactly that account. Other Docker profiles
write no sudoers rule and receive Docker's `no-new-privileges` guard. The
`control` agent space has no `sudo` option, because it has no entrypoint to grant anything.

This is root inside the container. Combined with `mounts`, it reaches whatever
those mounts expose. It is a separate decision from `docker: full`, and neither
implies the other.

### Docker access

`docker: full` is a separate opt-in from the controller's own Docker authority. The controller's socket lets it create worker containers. This option also gives the worker its own access to the daemon, which is host root. Peen mounts the controller's socket at `/var/run/docker.sock` in the worker and adds the socket's owning group, read from the socket itself, to the worker's supplementary groups, so the worker reaches the daemon without running as root. Peen refuses to start while a profile sets `docker: full` and the socket is owned by group 0. See [Docker profiles](where-it-runs.md#docker-profiles).

### Worker sockets

`PEEN_WORKER_SOCKET_DIR` is the root, and it defaults to
`PEEN_STATE_DIR/workers`. Each session gets its own directory beneath it,
`<root>/<session-uuid>/worker.sock`, and a Docker worker is given that one
directory rather than the root. The root lists every live session's socket, so
mounting it would show one worker where every other session's controller surface
lives.

A Unix socket address holds 107 bytes, and the root plus the session directory
and file name has to fit inside that. Peen measures it at startup and refuses to
start with the length it computed, so a deep state directory is fixed by naming
a shorter `PEEN_WORKER_SOCKET_DIR` rather than by finding the limit when a client
sends its first message.

## Logging and audit trail

`LOG_LEVEL` controls which structured application records go to stdout. The
audit sink retains debug-and-above records in the active UTC-day file,
`PEEN_LOG_DIRECTORY/YYYYMMDD-000000.log`. When `PEEN_LOG_DIRECTORY` is empty,
the directory defaults to `PEEN_STATE_DIR/logs`; set a path to override it. Keep
it out of `PEEN_CONFIG_DIR`, which every worker can read.
`PEEN_LOG_RETENTION` is a Go duration (default `336h`) and keeps that many days of daily files, so `336h` keeps at most 14 files. Go durations have no day unit, so write days as hours: `168h` is 7 days, `720h` is 30 days. The value must be a positive whole number of days, a multiple of `24h`, because files are per UTC day. `0` means the default. Startup fails on a negative value or one that is not a multiple of `24h`. The directory and
files are created as `0700` and `0600` respectively.

[ctxscope](https://github.com/psyb0t/ctxscope) carries request, session, turn,
child-agent, model, tool-call, and service fields through the log chain. Audit
records name the resolved harness manifest, skill activation, hook actions,
child-agent lifecycle, provider and tool outcomes, plus content byte counts and
SHA-256 digests.

The agent loop runs in a session's worker, so those records are produced in
another process. The controller reads each worker's output and re-emits it
through its own logging stack at the level the worker used, tagged with
`session_id` and `worker_generation_id`. That is what puts them in this file. A
worker receives neither the audit directory nor the state directory, so it never
writes here itself and never sees another session's records. Raw user prompts, model thinking, tool arguments, tool results,
environment values, and credentials are not copied into logs. Hook records may
include bounded operational counters such as the active-context token estimate.
The durable SQLite transcript retains the sensitive, verbatim trace for
authorized debugging, including model requests, responses, usage, costs, tool
definitions, child-agent runs, and compactions. ORM SQL statement previews are
deliberately disabled because expanded statements can contain persisted
sensitive content.

## Providers

`PEEN_UPSTREAMS` is a JSON array with one entry per provider. The docs call an entry an upstream or a provider, and the API calls its name `connectionName`. See [Terms](architecture.md#terms).

```bash
PEEN_UPSTREAMS=[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
AIGATE_TOKEN=your-token-here
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Local name, the prefix in `name/model`, such as `aigate/your-model-id`. No `/`. Unique across entries. |
| `type` | yes | Wire protocol: `openai`, `anthropic`, or `zai-coding`. |
| `baseUrl` | no | API base URL. Left out, the protocol's own default endpoint is used. |
| `apiKeyEnv` | no | Name of the environment variable that holds the key. Never put the key itself here. If set, that variable must be set and non-empty, or Peen refuses to start. |

Peen asks every listed provider for its models at startup. Delete every entry you do not use, together with its key variable. An entry whose key variable is missing or empty stops Peen from starting. An entry with a placeholder key fails discovery. Peen logs a warning and offers none of its models, and startup fails if `PEEN_DEFAULT_MODEL` or `PEEN_COMPACTION_MODEL` names one of them.

## Model selection

`PEEN_UPSTREAMS` accepts `openai`, `anthropic`, and `zai-coding` provider
types. The `.env.example` shows an OpenAI-compatible
[AIGate](https://github.com/psyb0t/aigate) entry and Z.ai's Coding endpoint.
`zai-coding` preserves Z.ai thinking state through tool rounds and applies its
model-specific thinking controls through
[Elelem](https://github.com/psyb0t/elelem).

Set `PEEN_DEFAULT_MODEL=zai/glm-5.3` for the default coding model and
`PEEN_COMPACTION_MODEL=zai/glm-5.3-flash` for the summarization call that
[compaction](#compaction) makes.
Model choice is per message. Peen does not classify requests or select a model
automatically.

The maintained Z.ai Coding catalog contains GLM 5.3 and GLM 5.3 Flash. GLM 5.3
supports its documented reasoning effort values. GLM 5.3 Flash uses preserved
thinking without numeric or disabling controls.

## Metrics

`GET /metrics` exposes Peen's application-owned
[Prometheus](https://github.com/prometheus/client_golang) registry on
`PEEN_METRICS_LISTEN_ADDRESS`. It never appears on the public API listener,
is not part of `/v1`, and is not governed by `PEEN_API_TOKEN` because the
listener itself is the access boundary.

The value must be an explicit non-zero loopback IP address and port, such as
`127.0.0.1:9090` or `[::1]:9090`. Wildcard and non-loopback addresses are
rejected at startup. Run a scraper in the same network namespace as Peen. A
Docker `-p` rule does not make a container loopback listener reachable from
the host.

## Context and turn limits

Every time setting is a Go duration such as `90s`, `10m`, or `1h30m`. A bare number such as `120` and an empty value are rejected at startup. `PEEN_TURN_TIMEOUT` and `PEEN_COMPACTION_TIMEOUT` must be positive. For `PEEN_TOOL_TIMEOUT`, `PEEN_TOOL_COMMAND_TIMEOUT`, `PEEN_TOOL_MAX_COMMAND_TIMEOUT`, and `PEEN_HOOK_COMMAND_TIMEOUT`, `0` means the default and a negative value fails startup. For `PEEN_LOG_RETENTION`, `0` means the default, as [logging](#logging-and-audit-trail) explains.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CONTEXT_TOKENS` | `32768` | [Elelem](https://github.com/psyb0t/elelem)'s request budget. Also the context size for a model whose driver publishes none; rejected at startup if larger than a published window. |
| `PEEN_COMPACTION_MODE` | `drop-oldest` | `drop-oldest` or `summarize`. See [compaction](#compaction). |
| `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` | `2048` | Reserved space for a generated summary. Must be smaller than `PEEN_MAX_CONTEXT_TOKENS`, validated even when `drop-oldest` is active. |
| `PEEN_COMPACTION_TIMEOUT` | `2m` | Bound on the separate summarization call. |
| `PEEN_TURN_TIMEOUT` | `10m` | Bound on one turn. |
| `PEEN_MAX_CONCURRENT_TURNS` | `16` | Global cap on turns running at once, across every session in the process. |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `16` | Messages sent while a turn is running wait until the model's current round ends. At most this many can wait at once, counting each skill message or already-loaded note that follows a queued message. A long turn can accept more in total, because delivery frees slots. Each one is reported with `userMessage.queued` and then `userMessage.delivered`. See [queued messages](http-api.md#send-and-watch-turns-over-websocket). |

### Compaction

A conversation grows until a request no longer fits in `PEEN_MAX_CONTEXT_TOKENS`. Compaction is what Peen does then, and `PEEN_COMPACTION_MODE` picks how:

- `drop-oldest` leaves whole old conversation units out of the request and stores nothing. The transcript keeps every message.
- `summarize` replaces an old completed part of the conversation with a summary made by `PEEN_COMPACTION_MODEL`, stores the summary, and never deletes transcript rows. `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` reserves room for the summary, and `PEEN_COMPACTION_TIMEOUT` limits the call. An optional `COMPACTION.md` in `PEEN_CONFIG_DIR` replaces the built-in summarizer instructions. Only this mode runs the `preCompact` and `postCompact` [hooks](hooks.md).

Under `summarize` a child agent compacts its own transcript into its own stored summaries, separate from the session's. `GET /v1/session/compactions` and `GET /v1/session/agents/{agentRunId}/compactions` read them. Peen validates the summary settings in both modes, so switching modes cannot expose a setting startup already accepted.

## Message size bounds

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_MESSAGE_BYTES` | `262144` | Bounds the `message` field in a `message.send` WebSocket frame. |
| `PEEN_MAX_SYSTEM_PROMPT_BYTES` | `65536` | Bounds the optional [`systemPrompt.content`](http-api.md#send-and-watch-turns-over-websocket) field of a `message.send` frame. |
| `PEEN_MAX_STORED_MESSAGE_BYTES` | `1048576` | Bounds any stored message row of any role, since an assistant or tool message is not bounded by a caller-facing setting. |

## Tool execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_TOOL_ROUNDS` | `32` | Tool-call rounds allowed in one turn. |
| `PEEN_MAX_CONCURRENT_TOOLS` | `4` | Tool calls one turn may run at once. |
| `PEEN_TOOL_TIMEOUT` | `15m` | Bound on one tool call, including a `runCommand` wait. |
| `PEEN_MAX_TOOL_RESULT_TOKENS` | `8192` | Bounds a tool result before it re-enters context. |
| `PEEN_TOOL_MAX_LIST_ENTRIES` | `1000` | `listFiles` entry cap. |
| `PEEN_TOOL_MAX_LIST_DEPTH` | `16` | `listFiles` recursion depth cap. |
| `PEEN_TOOL_MAX_SEARCH_MATCHES` | `200` | `searchText` match cap. |
| `PEEN_TOOL_MAX_SEARCH_FILE_BYTES` | `2097152` | Largest file `searchText` will scan. |
| `PEEN_TOOL_MAX_READ_BYTES` | `262144` | `readFile` byte cap per call. |
| `PEEN_TOOL_MAX_READ_LINES` | `2000` | `readFile` line cap per call. |
| `PEEN_TOOL_MAX_WRITE_BYTES` | `4194304` | `writeFile` size cap. |
| `PEEN_TOOL_MAX_EDITS` | `64` | Replacements allowed in one `editFile` call. |
| `PEEN_TOOL_MAX_DIFF_BYTES` | `65536` | `editFile` returned-diff cap. |
| `PEEN_TOOL_MAX_REMOVE_ENTRIES` | `20000` | `removePath` recursive entry cap. |
| `PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES` | `65536` | `runCommand` captured stdout/stderr cap. |
| `PEEN_TOOL_COMMAND_TIMEOUT` | `2m` | Default `runCommand` wait bound. The command is never killed when this elapses; a still-running command returns a job handle instead. |
| `PEEN_TOOL_MAX_COMMAND_TIMEOUT` | `15m` | Largest wait bound a call may request. Must not exceed `PEEN_TOOL_TIMEOUT`. |

## Hook execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_HOOK_COMMAND_TIMEOUT` | `30s` | Bound on one `command` hook action. Must not exceed `PEEN_TOOL_TIMEOUT`. |
| `PEEN_MAX_HOOK_COMMAND_OUTPUT` | `65536` | Maximum stdout or stderr captured from one hook command. |

See [hook configuration](hooks.md) for the file format, matching rules, event
order, and execution policy.

## Session notices

Background jobs, child agents, hooks, and outside callers report what happened through session notices, also called [session events](events.md). Peen hands them to the model at the next turn or tool boundary, or starts a turn when an event handler allows a wake.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_PENDING_EVENTS` | `256` | Per-session cap on the in-memory copy of pending notices. The oldest copy is dropped and counted in metrics. Delivery to the model reads stored notices from SQLite, so this cap never drops what the model receives. |
| `PEEN_MAX_EVENT_SUMMARY_BYTES` | `4096` | Largest `summary` on a notice. `POST /v1/session/notices` rejects a longer one with `400 VALIDATION_FAILED`, and a hook that emits one fails its action. Peen shortens its own `job.*` and `agent.*` summaries to fit, without splitting a character. |
| `PEEN_MAX_EVENT_DATA_BYTES` | `65536` | Largest encoded `data` on a notice. `POST /v1/session/notices` rejects larger data with `400 VALIDATION_FAILED`, and a hook that emits it fails its action. Peen leaves over-size data out of its own `job.*` and `agent.*` events. |
| `PEEN_MAX_EVENT_WAKES_PER_HOUR` | `60` | Per-session cap on turns started by a wake. An event over the cap stays queued for the next turn or tool boundary. `0` removes the cap. Peen keeps the count in memory, so it starts over when Peen restarts. |

See [session events](events.md) for event types, delivery, wakes, and event handlers.

## Child agent limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `5` | How many `launchAgent` calls may nest. A child at that depth does not receive `launchAgent`. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child conversation may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Agent runs one session may have in flight at once. |
| `PEEN_MAX_AGENT_RUN_EVENT_COUNT` | `2000` | How many of a running child's recent events Peen keeps in memory for clients following it live. SQLite keeps every event regardless. |
| `PEEN_MAX_AGENT_RUN_EVENT_BYTES` | `65536` | Byte cap on that same in-memory list. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size cap on an inline `agentDefinition.instructions` string. |

## Harness layering

Rules, skills, named agents, event handlers, and hooks are files Peen reads from the configuration directory and from every directory between `/` and the workspace, fresh at the start of every turn. [The harness guide](harness.md) covers the layer order, how layers combine, what the model sees, and the limits. Each piece has its own page: [rules](rules.md), [skills](skills.md), [named agents](agents.md), [session events and event handlers](events.md), and [hooks](hooks.md).

Every root and child turn also receives the server's current local timestamp,
timezone, operating system, CPU architecture, logical CPU count, and Go
runtime as trusted runtime context. The embedded freshness guidance tells the
model to inspect local project facts and verify external facts that may have
changed.

## Process jobs

`runCommand` never kills a process merely because a wait bound elapsed. A
command that exits within `PEEN_TOOL_COMMAND_TIMEOUT` (or a caller-requested
`timeout` up to `PEEN_TOOL_MAX_COMMAND_TIMEOUT`) returns its exit status and
output normally. A command still running when the bound expires is left
running, and the tool returns a job handle plus the output captured so far.
An explicit `background` argument skips the wait and returns the handle
immediately. `runCommand` and `waitJob` take `timeout` as a Go duration string such as `"90s"`, `"5m"`, or `"1h30m"`. Leaving it out uses the default, and `0s`, negative values, and bare numbers are rejected. For `runCommand` the default is `PEEN_TOOL_COMMAND_TIMEOUT`, and a value above `PEEN_TOOL_MAX_COMMAND_TIMEOUT` is lowered to that maximum rather than rejected. For `waitJob` the default is `30s` and Peen lowers any value above `10m` to `10m`. These two `waitJob` bounds are fixed and no `PEEN_` variable changes them.

Jobs are session-scoped, not turn-scoped: a command started in one turn stays
listable, readable, and signalable from a later turn, and cancelling the turn
that started it does not stop it. Peen writes every observed stdout and stderr
line to SQLite before it enters a bounded live ring buffer. The buffer is only
for the active tool call. REST output replay, job metadata, and signal history
come from SQLite and survive reconnects and restarts. On shutdown, Peen stops
every running job gracefully, waits a grace period, then kills its process
group; no job silently outlives the process. A job that finishes after its
`runCommand` call returned publishes a `job.exited`, `job.signalled`, or
`job.failed` [session event](events.md).

## Agent run observability

Each `launchAgent` call creates an immutable SQLite run record and durable
event records. Use the session agent-run REST endpoints to inspect or cancel a
run and to read its events after its parent turn has finished. Peen has no
JSONL transcript mirror. SQLite is the sole durable replay store.
