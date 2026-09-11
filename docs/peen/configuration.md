# Configuration reference

Peen reads typed `PEEN_`-prefixed environment variables, validated once at
startup; a bad value fails before the listener opens. `.env.example` is the
canonical, commented list. This page groups the same settings by purpose and
adds the harness, event, job, and agent-run behavior they control.

Framework-level logging (`LOG_LEVEL`, `LOG_FORMAT`, `LOG_ADD_SOURCE`) is
handled by the underlying Servicepack logging setup. Peen adds a JSON audit
sink configured by `PEEN_LOG_DIRECTORY` and `PEEN_LOG_RETENTION_DAYS`.

## Core

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_CONFIG_DIR` | required, absolute | Harness base layer and durable state root. See [the root README](../../README.md#the-configuration-directory). |
| `PEEN_WORKING_DIR` | process cwd at startup | Default message workspace. Peen changes into this directory at startup. |
| `PEEN_AGENT` | `default` | Root agent name. `default` is embedded and may be replaced by `.agents/agents/default.md`. |
| `PEEN_UPSTREAMS` | required, JSON | Named provider list. See [provider configuration](../../README.md#provider-configuration). |
| `PEEN_DEFAULT_MODEL` | required | Qualified `provider/model` for the root agent and, unless overridden, compaction. |
| `PEEN_COMPACTION_MODEL` | `PEEN_DEFAULT_MODEL` | Qualified `provider/model` used only for the summarization call. |
| `PEEN_HTTP_LISTEN_ADDRESS` | `:8080` | Listener address. |
| `PEEN_API_TOKEN` | empty | Bearer token. Empty disables authentication. |
| `PEEN_METRICS_LISTEN_ADDRESS` | `127.0.0.1:9090` | Separate loopback-only Prometheus listener. See [metrics](#metrics). |

## Logging and audit trail

`LOG_LEVEL` controls which structured application records go to stdout. The
audit sink retains debug-and-above records in the active UTC-day file,
`PEEN_LOG_DIRECTORY/YYYYMMDD-000000.log`. When `PEEN_LOG_DIRECTORY` is empty,
the directory defaults to `PEEN_CONFIG_DIR/logs`; set a path to override it.
`PEEN_LOG_RETENTION_DAYS=14` keeps at most 14 daily files. The directory and
files are created as `0700` and `0600` respectively.

`ctxscope` carries request, session, turn, child-agent, model, tool-call, and
service fields through the log chain. Audit records name the resolved harness
manifest, skill activation, hook actions, child-agent lifecycle, provider and
tool outcomes, plus content byte counts and SHA-256 digests. Raw user prompts,
model thinking, tool arguments, tool results, environment values, and
credentials are not copied into logs. Hook records may include bounded
operational counters such as the active-context token estimate. The durable
session transcript and per-agent JSONL mirror retain the sensitive, verbatim
trace for authorized debugging. ORM SQL statement previews are deliberately
disabled because expanded statements can contain persisted sensitive content.

## Model selection

`PEEN_UPSTREAMS` accepts `openai`, `anthropic`, and `zai-coding` provider
types. The `.env.example` shows an OpenAI-compatible AIGate entry and Z.ai's
Coding endpoint. `zai-coding` preserves Z.ai thinking state through tool rounds
and applies its model-specific thinking controls.

Set `PEEN_DEFAULT_MODEL=zai/glm-5.3` for the default coding model and
`PEEN_COMPACTION_MODEL=zai/glm-5.3-flash` for lightweight background work.
Model choice is per message. Peen does not classify requests or select a model
automatically.

The maintained Z.ai Coding catalog contains GLM 5.3 and GLM 5.3 Flash. GLM 5.3
supports its documented reasoning effort values. GLM 5.3 Flash uses preserved
thinking without numeric or disabling controls.

## Metrics

`GET /metrics` exposes Peen's application-owned Prometheus registry on
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
| `PEEN_MAX_CONTEXT_TOKENS` | `32768` | Elelem's request budget. Also the context size for a model whose driver publishes none; rejected at startup if larger than a published window. |
| `PEEN_COMPACTION_MODE` | `drop-oldest` | `drop-oldest` or `summarize`. See [compaction modes](../../README.md#compaction-modes). |
| `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` | `2048` | Reserved space for a generated summary. Must be smaller than `PEEN_MAX_CONTEXT_TOKENS`, validated even when `drop-oldest` is active. |
| `PEEN_COMPACTION_TIMEOUT` | `2m` | Bound on the separate summarization call. |
| `PEEN_TURN_TIMEOUT` | `10m` | Bound on one turn. |
| `PEEN_MAX_CONCURRENT_TURNS` | `16` | Global cap on turns running at once, across every session in the process. |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `16` | Per active-turn cap for caller messages waiting for Elelem's next provider round boundary. |

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

## Session events

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_PENDING_EVENTS` | `256` | Per-session queue depth. Oldest is dropped first, and the drop is counted. |
| `PEEN_MAX_EVENT_SUMMARY_BYTES` | `4096` | Bounds `summary` on a posted event. |
| `PEEN_MAX_EVENT_DATA_BYTES` | `65536` | Bounds `data` on a posted event. |
| `PEEN_MAX_EVENT_WAKES_PER_HOUR` | `60` | Per-session cap on `wake`-started turns. Wakes over the bound coalesce into the next queued delivery instead of starting more turns. |

## Child agent limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `3` | How many `launch_agent` calls may nest. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child conversation may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Agent runs one session may have in flight at once. |
| `PEEN_MAX_AGENT_RUN_EVENT_COUNT` | `2000` | Events kept in a run's in-memory follow buffer. |
| `PEEN_MAX_AGENT_RUN_EVENT_BYTES` | `65536` | Byte cap on that same buffer. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size cap on an inline `agentDefinition.instructions` string. |

## Harness layering

Peen resolves `AGENTS.md`, skills, named agents, and event handlers in the
same order, every turn:

1. Embedded operating rules, the `planning` and `freshness` skills, and the
   `default` root agent are the immutable base layer.
2. `PEEN_CONFIG_DIR` extends the base layer.
3. Every filesystem ancestor of the current message's workspace is then
   applied, from `/` down to the workspace itself.
4. At each filesystem layer, `AGENTS.md` and `.agents/` are read before moving to the
   next, more specific layer.

A missing layer is normal. An unreadable or malformed layer that does exist
is a hard startup or turn error, never a silent skip. Entries are sorted
bytewise for stable, repeatable results.

- **`AGENTS.md`**: each file is kept as its own instruction block in layer
  order. A message's own text cannot rewrite these blocks.
- **Skills** (`.agents/skills/<name>/SKILL.md`): only the name and
  description are placed in the system prompt at turn start (progressive
  disclosure). `use_skill` loads one full `SKILL.md` and its source directory
  on demand; files it references are then read with the normal `read_file`
  tool, so that read is a visible, ordinary tool call. A same-named skill in
  a later layer replaces the earlier or embedded one as a whole unit; they are
  never merged. `homepage`, `user-invocable`, `permissions`, and nested
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

## Session events

`POST /v1/session/events` is how something outside Peen (a webhook, a CI
job, an operator) tells a running session something happened. `type` uses
the `job.` and `agent.` prefixes reserved for Peen's own producers
(`job.exited`, `job.signalled`, `job.failed`, `agent.finished`,
`agent.failed`); anything else is the deployment's to define.

`delivery: queue` (default) waits for the next turn or tool boundary.
`delivery: wake` starts a turn immediately if the session is idle and a
matching `.agents/events/<type>.md` handler exists; a busy session degrades
the wake to `queue`, and an unhandled type starts nothing. Event `summary`
and `data` always reach the model quoted as data under a header naming their
source, never merged into the system prompt: event content is untrusted
input.

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
that started it does not stop it. Output is captured into bounded ring
buffers per stream, oldest lines dropped first. On shutdown, Peen stops every
running job gracefully, waits a grace period, then kills its process group;
no job silently outlives the process.

## Agent run observability

Each `launch_agent` call is mirrored, one JSONL line per event, to:

```text
<PEEN_CONFIG_DIR>/transcripts/<session-id>/agents/<agent-name>/<run-id>.jsonl
```

SQLite remains the authority for durable state; this file is an append-only
mirror for tailing and for reading back a finished run's events once its
in-memory follow buffer has been evicted. There is currently no equivalent
session-level transcript mirror alongside the main session's SQLite
transcript, only this per-run one.
