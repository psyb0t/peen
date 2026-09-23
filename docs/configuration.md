# Configuration

Copy `.env.example` to `.env`, then set the provider values that fit your
machine. Peen validates every `PEEN_` value before opening its listener, so a
bad setting fails at startup instead of halfway through a task. Peen starts
with no sessions. A client opens a workspace through `POST /v1/sessions/open`,
and `PEEN_WORKSPACE_ROOTS` bounds which directories it may name.

For Docker, `.env` is input for `docker run --env-file`. Do not source it from
Bash because `PEEN_UPSTREAMS` is raw JSON. For a bare binary, set the same
values through your shell, service manager, or secret manager. This page is
the full reference; the example file is the commented starting point.

`LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE` control application logging.
Peen adds a JSON audit sink configured by `PEEN_LOG_DIRECTORY` and
`PEEN_LOG_RETENTION_DAYS`.

## Start here

These values decide where Peen keeps its state and which model handles a task.
Start Peen from the directory the agent should work in. Docker users set that
directory with `docker run --workdir` and must use a literal host path when
they want Docker worker profiles. Bare-process users change directory before
launching Peen.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_CONFIG_DIR` | required, absolute | Global configuration and harness layer. Every worker receives it read-only. See [directories](#directories). |
| `PEEN_STATE_DIR` | required, absolute | Controller-owned durable state: SQLite, audit logs, worker sockets. No worker receives it. See [directories](#directories). |
| `PEEN_WORKSPACE_ROOTS` | process working directory | JSON array of absolute paths a client may open as a workspace. See [workspace roots](#workspace-roots). |
| `PEEN_EXECUTION_PROFILES` | one `native` profile | JSON array of runnable execution profiles a client may name. See [execution profiles](#execution-profiles). |
| `PEEN_DEFAULT_EXECUTION_PROFILE` | `native` | Profile a session opened without naming one uses. |
| `PEEN_WORKER_SOCKET_DIR` | `PEEN_STATE_DIR/workers` | Root holding one directory per session worker. A worker receives only its own. |
| `PEEN_DOCKER_SOCKET` | `DOCKER_HOST`, else `/var/run/docker.sock` | Docker socket the controller uses to create worker containers. Absent means Docker profiles are refused. |
| `PEEN_WORKER_IMAGE` | empty | Overrides the image for every Docker profile. Empty uses each profile's own `image`, and a profile without one runs the image published alongside this build. Set it to run a worker from a local build. |
| `PEEN_HOST_USERNAME` | empty | Host account name for a Docker controller started with numeric `--user` IDs. Set together with `PEEN_HOST_HOME`. |
| `PEEN_HOST_HOME` | empty | Absolute host home for a Docker controller started with numeric `--user` IDs. Set together with `PEEN_HOST_USERNAME`. |
| `PEEN_AGENT` | `default` | Root agent name. `default` is embedded and may be replaced by `.agents/agents/default.md`. |
| `PEEN_UPSTREAMS` | required, JSON | Named provider list. See [provider configuration](../README.md#provider-configuration). |
| `PEEN_DEFAULT_MODEL` | required | Qualified `provider/model` for the root agent and, unless overridden, compaction. |
| `PEEN_COMPACTION_MODEL` | `PEEN_DEFAULT_MODEL` | Qualified `provider/model` used only for the summarization call. |
| `PEEN_HTTP_LISTEN_ADDRESS` | `:8080` | Listener address. |
| `PEEN_API_TOKEN` | empty | Bearer token. Empty disables authentication. |
| `PEEN_METRICS_LISTEN_ADDRESS` | `127.0.0.1:9090` | Separate loopback-only Prometheus listener. See [metrics](#metrics). |

## Directories

Peen keeps two directories apart, and refuses to start if they overlap.

`PEEN_CONFIG_DIR` is the global configuration and harness layer: `AGENTS.md`,
`.agents/`, and the optional `SYSTEM.md`, `APPEND_SYSTEM.md`, and
`COMPACTION.md`. Every Docker worker receives this directory read-only, so
anything inside it is readable by every session.

`PEEN_STATE_DIR` is the controller's own durable state: `peen.db`, the audit
logs, and the worker socket root. No worker receives it.

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

## Workspace roots

`PEEN_WORKSPACE_ROOTS` is a JSON array of absolute paths:

```bash
PEEN_WORKSPACE_ROOTS='["/srv/work","/srv/scratch"]'
```

A client may open any directory that is a root or sits under one. Peen resolves
the requested path through its symlinks before checking it, so an alias of an
allowed directory opens the same session as the real path, and a symlink inside
a root that points outside it is refused. A path outside every root returns
`403 WORKSPACE_NOT_ALLOWED` and creates no session.

Leaving the variable unset allows only the process working directory, which
matches the single-workspace behavior this setting generalizes. Set it when one
Peen should serve several projects.

Peen refuses to start when a configured root is relative or missing, so a typo
fails at startup rather than when a client first opens a workspace.

## Execution profiles

A session's tools run under an operator-defined execution profile. A client
names a profile when it opens a workspace and never sends execution arguments.

`PEEN_EXECUTION_PROFILES` is a JSON array. Leaving it unset defines the single
`native` profile, so a deployment that configures nothing runs tools on the host
as it always has. `PEEN_DEFAULT_EXECUTION_PROFILE` names the profile a session
opened without one uses, and defaults to `native`.

A `native` profile runs the session worker as a child process of the controller,
using the same installed Peen binary. A `docker` profile runs it in a sibling
container named `peen-worker-<session-uuid>`.

Peen picks the worker image in one order: `PEEN_WORKER_IMAGE` if it is set, then
the profile's own `image`, then the image published alongside the running
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

A Docker profile needs the controller to reach a Docker socket. Peen decides
that at startup by checking `PEEN_DOCKER_SOCKET`, then a `unix://` `DOCKER_HOST`,
then `/var/run/docker.sock`. Without one, a session on a Docker profile is
refused. Peen never falls back to a native worker, because that would run the
model's tools on the host after the operator asked for a container.

```bash
PEEN_EXECUTION_PROFILES='[
  {"name":"native","kind":"native"},
  {"name":"sandbox","kind":"docker"},
  {"name":"host-like","kind":"docker",
   "allowNetwork":true,"allowDockerSocket":true}
]'
PEEN_DEFAULT_EXECUTION_PROFILE=native
```

| Field | Meaning |
| --- | --- |
| `name` | The name a client may send as `profile` when it opens a workspace. |
| `kind` | `native` or `docker`. |
| `image` | Docker only, optional. Any image reference the daemon can resolve. Empty runs the image published alongside this build. Overridden by `PEEN_WORKER_IMAGE` when that is set. |
| `mounts` | Extra host paths the worker container gets, each with `readOnly`. |
| `allowNetwork` | Docker only. False creates the container with networking disabled. |
| `allowDockerSocket` | Docker only. Mounts the host Docker socket into the worker. |
| `allowPrivilegeEscalation` | Docker only. Gives the worker account passwordless sudo and removes the worker's `no-new-privileges` guard. Reported as a capability warning. |
| `revision` | Records the profile definition version on every worker generation. |

A Docker worker runs as the controller's own host UID, GID, and username, so
files it writes in a mounted workspace keep host ownership. Peen refuses to start
a Docker worker as root. The workspace is mounted writable at its literal host
path, the config directory read-only at its literal path, and the session's own
socket directory writable, so a path in a transcript means the same thing inside
and outside the container.

A native controller reads its username and home from the operating system. A
controller itself running in Docker with `--user uid:gid` has no passwd entry
for that host account, so set `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` together.
Peen uses those values with the controller's numeric UID and GID to recreate the
same account inside each worker. A partial pair or a relative home fails startup.

A worker receives only the runtime configuration required to run a turn. This
includes the provider definitions, model selection, limits, and the named
provider credentials selected by `apiKeyEnv`. It never receives
`PEEN_STATE_DIR`, the public `PEEN_API_TOKEN`, controller Docker authority, or
the entrypoint's identity controls. The provider credential is available to the
worker's agent process, so do not give a Docker profile a workspace containing
secrets you would not expose to that process.

### Privilege escalation

`allowPrivilegeEscalation` gives the agent working sudo inside its own container.
The image installs sudo but ships no sudoers rule, so sudo authorizes nobody
until a profile asks for it.

Every Docker worker starts as root only for entrypoint bootstrap. The entrypoint
creates or reconciles the controller host UID, GID, and username inside the
image, then drops to that account with `setpriv` before the agent starts. This
keeps both host file ownership and the host username correct inside a worker.

For an escalating profile, the entrypoint also writes
`/etc/sudoers.d/peen-worker` for exactly that account. Other Docker profiles
write no sudoers rule and receive Docker's `no-new-privileges` guard. A `native`
profile cannot set the flag: there is no entrypoint to grant anything, so Peen
refuses the profile at startup rather than accepting a promise it cannot keep.

This is host-root-equivalent inside the container. Combined with `mounts`, it
reaches whatever those mounts expose. It is a separate decision from
`allowDockerSocket`, and neither implies the other.

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

`allowDockerSocket` is a separate opt-in from the controller's own Docker
authority. The controller's socket lets it create worker containers. This option
additionally gives the worker its own access to the daemon, which is
host-root-equivalent. Peen adds the socket's owning group to the worker's
supplementary groups, read from the socket itself, so the worker reaches the
daemon without running as root. A socket owned by group 0 is refused.

Naming a profile the operator did not define returns 403 and creates no session.
Opening an existing session never changes the profile it already runs under.

Moving an existing session to another profile is a separate operation,
`POST /v1/session/reconfigure`, and it requires a reason. Peen records every
change in `session_profile_decisions` with the profile the session came from,
the profile it moved to, the reason, and the time. A session with a turn in
flight is refused until that turn ends. On success Peen stops the session's
current worker, so the next turn starts a new generation under the new
profile. `GET /v1/session/profile-decisions` reads that history back.

## Logging and audit trail

`LOG_LEVEL` controls which structured application records go to stdout. The
audit sink retains debug-and-above records in the active UTC-day file,
`PEEN_LOG_DIRECTORY/YYYYMMDD-000000.log`. When `PEEN_LOG_DIRECTORY` is empty,
the directory defaults to `PEEN_STATE_DIR/logs`; set a path to override it. Keep
it out of `PEEN_CONFIG_DIR`, which every worker can read.
`PEEN_LOG_RETENTION_DAYS=14` keeps at most 14 daily files. The directory and
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

## Model selection

`PEEN_UPSTREAMS` accepts `openai`, `anthropic`, and `zai-coding` provider
types. The `.env.example` shows an OpenAI-compatible
[AIGate](https://github.com/psyb0t/aigate) entry and Z.ai's Coding endpoint.
`zai-coding` preserves Z.ai thinking state through tool rounds and applies its
model-specific thinking controls through
[Elelem](https://github.com/psyb0t/elelem).

Set `PEEN_DEFAULT_MODEL=zai/glm-5.3` for the default coding model and
`PEEN_COMPACTION_MODEL=zai/glm-5.3-flash` for lightweight background work.
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

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CONTEXT_TOKENS` | `32768` | [Elelem](https://github.com/psyb0t/elelem)'s request budget. Also the context size for a model whose driver publishes none; rejected at startup if larger than a published window. |
| `PEEN_COMPACTION_MODE` | `drop-oldest` | `drop-oldest` or `summarize`. See [conversation limits](../README.md#things-worth-knowing). |
| `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` | `2048` | Reserved space for a generated summary. Must be smaller than `PEEN_MAX_CONTEXT_TOKENS`, validated even when `drop-oldest` is active. |
| `PEEN_COMPACTION_TIMEOUT` | `2m` | Bound on the separate summarization call. |
| `PEEN_TURN_TIMEOUT` | `10m` | Bound on one turn. |
| `PEEN_MAX_CONCURRENT_TURNS` | `16` | Global cap on turns running at once, across every session in the process. |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `16` | Per active-turn cap for caller messages waiting for [Elelem](https://github.com/psyb0t/elelem)'s next provider round boundary. |

## Message size bounds

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_MESSAGE_BYTES` | `262144` | Bounds the `message` field in a `message.send` WebSocket event. |
| `PEEN_MAX_SYSTEM_PROMPT_BYTES` | `65536` | Bounds `systemPrompt.content`. |
| `PEEN_MAX_STORED_MESSAGE_BYTES` | `1048576` | Bounds any stored message row of any role, since an assistant or tool message is not bounded by a caller-facing setting. |

## Tool execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_TOOL_ROUNDS` | `32` | Tool-call rounds allowed in one turn. |
| `PEEN_MAX_CONCURRENT_TOOLS` | `4` | Tool calls one turn may run at once. |
| `PEEN_TOOL_TIMEOUT` | `15m` | Bound on one tool call, including a `run_command` wait. |
| `PEEN_MAX_TOOL_RESULT_TOKENS` | `8192` | Bounds a tool result before it re-enters context. |
| `PEEN_TOOL_MAX_LIST_ENTRIES` | `1000` | `list_files` entry cap. |
| `PEEN_TOOL_MAX_LIST_DEPTH` | `16` | `list_files` recursion depth cap. |
| `PEEN_TOOL_MAX_SEARCH_MATCHES` | `200` | `search_text` match cap. |
| `PEEN_TOOL_MAX_SEARCH_FILE_BYTES` | `2097152` | Largest file `search_text` will scan. |
| `PEEN_TOOL_MAX_READ_BYTES` | `262144` | `read_file` byte cap per call. |
| `PEEN_TOOL_MAX_READ_LINES` | `2000` | `read_file` line cap per call. |
| `PEEN_TOOL_MAX_WRITE_BYTES` | `4194304` | `write_file` size cap. |
| `PEEN_TOOL_MAX_EDITS` | `64` | Replacements allowed in one `edit_file` call. |
| `PEEN_TOOL_MAX_DIFF_BYTES` | `65536` | `edit_file` returned-diff cap. |
| `PEEN_TOOL_MAX_REMOVE_ENTRIES` | `20000` | `remove_path` recursive entry cap. |
| `PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES` | `65536` | `run_command` captured stdout/stderr cap. |
| `PEEN_TOOL_COMMAND_TIMEOUT` | `2m` | Default `run_command` wait bound. The command is never killed when this elapses; a still-running command returns a job handle instead. |
| `PEEN_TOOL_MAX_COMMAND_TIMEOUT` | `15m` | Largest wait bound a call may request. Must not exceed `PEEN_TOOL_TIMEOUT`. |

## Hook execution limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_ENABLE_WORKSPACE_HOOKS` | `false` | Allows executable actions from workspace `.agents/hooks.yaml` layers. Hooks under `PEEN_CONFIG_DIR` always run. |
| `PEEN_HOOK_COMMAND_TIMEOUT` | `30s` | Bound on one `command` hook action. Must not exceed `PEEN_TOOL_TIMEOUT`. |
| `PEEN_MAX_HOOK_COMMAND_OUTPUT` | `65536` | Maximum stdout or stderr captured from one hook command. |

See [hook configuration](hooks.md) for the file format, matching rules, event
order, and execution policy.

## Session notices and delivery limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_PENDING_EVENTS` | `256` | Per-session notice-queue depth. Oldest is dropped first, and the drop is counted. |
| `PEEN_MAX_EVENT_SUMMARY_BYTES` | `4096` | Bounds `summary` on a posted notice. |
| `PEEN_MAX_EVENT_DATA_BYTES` | `65536` | Bounds `data` on a posted notice. |
| `PEEN_MAX_EVENT_WAKES_PER_HOUR` | `60` | Per-session cap on `wake`-started turns. Wakes over the bound coalesce into the next queued delivery instead of starting more turns. |

## Child agent limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `5` | How many `launch_agent` calls may nest. A child at that depth does not receive `launch_agent`. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child conversation may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Agent runs one session may have in flight at once. |
| `PEEN_MAX_AGENT_RUN_EVENT_COUNT` | `2000` | Events kept in a run's in-memory follow buffer. |
| `PEEN_MAX_AGENT_RUN_EVENT_BYTES` | `65536` | Byte cap on that same buffer. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size cap on an inline `agentDefinition.instructions` string. |

## Harness layering

Peen resolves standing instructions, skills, named agents, and event handlers
in the same order, every turn:

1. Embedded operating rules, the `planning` and `freshness` skills, and the
   `default` root agent are the immutable base layer.
2. `PEEN_CONFIG_DIR` extends the base layer.
3. Every filesystem ancestor of the session's workspace is then
   applied, from `/` down to the workspace itself.
4. At each filesystem layer, Peen reads `AGENTS.md`, then sorted
   `.claude/rules/*.md`, then sorted `.agents/rules/*.md`, then compatible
   `.claude/skills/` and native `.agents/` definitions before moving to the
   next, more specific layer.

A missing layer is normal. An unreadable or malformed layer that does exist
is a hard startup or turn error, never a silent skip. Entries are sorted
bytewise for stable, repeatable results.

- **`AGENTS.md`**: each file is kept as its own instruction block in layer
  order. A message's own text cannot rewrite these blocks.
- **Modular rules** (`.claude/rules/<name>.md` and
  `.agents/rules/<name>.md`): every direct non-empty Markdown file is an
  additive, always-on instruction block. Missing directories are normal.
  An empty file, unreadable path, or a directory masquerading as a Markdown
  rule is a hard error. Claude-compatible rules load before native rules at
  one layer. Rules are never replacements for an earlier rule file.
- **Skills** (`.claude/skills/<name>/SKILL.md` or
  `.agents/skills/<name>/SKILL.md`): only the name and
  description are placed in the system prompt at turn start (progressive
  disclosure). `use_skill` loads one full `SKILL.md` and its source directory
  on demand; files it references are then read with the normal `read_file`
  tool, so that read is a visible, ordinary tool call. A same-named skill in
  a later layer replaces the earlier or embedded one as a whole unit; a native
  `.agents` skill wins over the compatible `.claude` skill in one layer. They
  are never merged. A user can write a standalone `:skill-name` reference at
  the start of a message or after whitespace to require that exact effective
  skill. Peen rejects an unknown name before opening a turn or contacting a
  provider, then injects the full document into the root and child-agent
  prompt. The original user text remains unchanged. A queued message cannot
  directly activate a skill because the running turn's prompt is already
  fixed. Without that syntax, the model uses the catalogue name and
  description to decide whether to call `use_skill`. `homepage`,
  `user-invocable`, `permissions`, and nested
  `metadata` are accepted and retained in the resolved skill record.
  `allowed-tools` and `permissions` are advisory only. Peen has no permission
  layer, so it cannot narrow which tools a skill's turn may call.
- **Named agents** (`.agents/agents/<name>.md`): YAML frontmatter with `name`
  and `description`, lowercase kebab-case, followed by system instructions.
  An optional `allowed-tools` string is a comma-separated allowlist enforced
  for that stored agent, including a replacement `default` root agent. Omit it
  to expose the normal host tool set. `launch_agent` runs one by name, or
  accepts an inline `agentDefinition` for a one-off job no stored file covers.
  Exactly one of the two must be supplied. A child shares the parent turn's
  session, workspace, resolved rules, and model; it cannot select its own
  model or provider.
- **Event handlers** (`.agents/events/<type>.md`): frontmatter with `type`,
  an optional `agent` naming which effective agent handles it, and an
  optional `delivery` override. The body is the instruction the agent
  receives when that event type arrives.
- **Hooks** (`.agents/hooks.yaml`): additive ordered action groups. A hook
  document under `PEEN_CONFIG_DIR` is executable. Documents from workspace
  ancestor layers are resolved, hashed, and listed in the context manifest,
  but their actions only execute when `PEEN_ENABLE_WORKSPACE_HOOKS=true`.
  See [hook configuration](hooks.md).

Every root and child turn also receives the server's current local timestamp,
timezone, operating system, CPU architecture, logical CPU count, and Go
runtime as trusted runtime context. The embedded freshness guidance tells the
model to inspect local project facts and verify external facts that may have
changed.

## Session notices

`POST /v1/session/notices` is how something outside Peen (a webhook, a CI
job, an operator) tells a running session something happened. `type` uses
the `job.` and `agent.` prefixes reserved for Peen's own producers
(`job.exited`, `job.signalled`, `job.failed`, `agent.finished`,
`agent.failed`); anything else is the deployment's to define.

`delivery: queue` (default) waits for the next turn or tool boundary.
`delivery: wake` starts a turn immediately if the session is idle and a
matching `.agents/events/<type>.md` handler exists; a busy session degrades
the wake to `queue`, and an unhandled type starts nothing. Notice `summary`
and `data` always reach the model quoted as data under a header naming their
source, never merged into the system prompt: notice content is untrusted
input. `GET /v1/session/events` is separate. It replays Peen's durable
protocol transcript and never consumes or injects notices.

## Process jobs

`run_command` never kills a process merely because a wait bound elapsed. A
command that exits within `PEEN_TOOL_COMMAND_TIMEOUT` (or a caller-requested
bound up to `PEEN_TOOL_MAX_COMMAND_TIMEOUT`) returns its exit status and
output normally. A command still running when the bound expires is left
running, and the tool returns a job handle plus the output captured so far.
An explicit `background` argument skips the wait and returns the handle
immediately.

Jobs are session-scoped, not turn-scoped: a command started in one turn stays
listable, readable, and signalable from a later turn, and cancelling the turn
that started it does not stop it. Peen writes every observed stdout and stderr
line to SQLite before it enters a bounded live ring buffer. The buffer is only
for the active tool call. REST output replay, job metadata, and signal history
come from SQLite and survive reconnects and restarts. On shutdown, Peen stops
every running job gracefully, waits a grace period, then kills its process
group; no job silently outlives the process.

## Agent run observability

Each `launch_agent` call creates an immutable SQLite run record and durable
event records. Use the session agent-run REST endpoints to inspect or cancel a
run and to read its events after its parent turn has finished. Peen has no
JSONL transcript mirror. SQLite is the sole durable replay store.
