# Architecture

Peen is a durable coding-agent service. A client opens a WebSocket, sends a
task for a named session, and watches the model, tools, hooks, and child agents
work. The default socket is a global event feed. Clients build session tabs by
filtering event metadata, or request one server-side session filter. Peen saves
the work as it happens, so reconnecting after a client or process restart does
not throw the conversation away.

```text
WebSocket client
      |
      v
control plane: HTTP server, session hub, SQLite, worker supervisor
      |                                         ^
      | run_turn over a private Unix socket     | durable writes, then events
      v                                         |
session worker process ------------------------ +
      |
      v
agent runtime <--> model provider
      |                  |
      |                  v
      |              model events
      v
harness, tools, hooks, child agents
      |
      +--> workspace files and commands
```

REST sits beside the WebSocket. It reads durable session state, lists messages,
protocol events, notices, compactions, model exchanges, jobs, and child-agent
runs, or cancels work. It never accepts a user task or starts a turn. [The API
reference](http-api.md) has the contract.

[Aichteeteapee](https://github.com/psyb0t/aichteeteapee) supplies Peen's HTTP
server, REST error envelope, and WebSocket hub. Peen gives every socket its own
server-controlled identity in that hub. The server fans a session's
events to global sockets and to sockets filtered for that session.

## Control plane and workers

One host runs one control plane. It owns SQLite, the REST listener, the global
WebSocket hub, session routing, and worker lifecycle. It does not run the model
loop. There are no controller IDs and no second controller.

Each session's turns run in a worker process the control plane starts on demand.
A worker never opens the control SQLite file. It reaches durable state through
the control plane over a private per-session Unix socket, which carries JSON
frames for `run_turn`, `cancel`, and `shutdown`. The socket is created with only
the controller's own access, and the worker authenticates with a credential
generated per worker generation. The control plane stores only the SHA-256 hash
of that credential, hands the raw value to the worker on stdin, and never writes
it to a command line, an environment variable, a label, or a log.

An operator defines the execution profiles a session may run under. The client
names a profile and nothing else. Images, mounts, network, the Docker socket,
and privilege escalation come from deployment configuration. A native profile
starts the same installed Peen binary with its internal `worker` command. A
Docker profile creates a container named `peen-worker-<session-uuid>` carrying
exactly two labels, `peen.managed=true` and `peen.session=<session-uuid>`, and
the supervisor acts on a stored container ID only when both labels still match.

A Docker worker runs as the controller's own host UID, GID, and username. A
native controller resolves that account from the operating system. A controller
running in Docker with numeric `--user` IDs supplies `PEEN_HOST_USERNAME` and
`PEEN_HOST_HOME` so a worker can recreate the same host account. Every Docker
worker container starts as root just long enough for the image entrypoint to
create that account and drop to it. A profile that sets
`allowPrivilegeEscalation` also gets a passwordless sudo rule for that account.
The agent is the host user either way. See [privilege
escalation](configuration.md#privilege-escalation).

A Docker worker calls the model provider itself, so a Docker profile needs
`allowNetwork: true` to reach a provider over the network.

The controller passes each Docker worker the runtime configuration it needs to
execute a turn, including provider definitions and named provider credentials.
It never passes controller SQLite state, the public API token, the controller
Docker socket, or the entrypoint bootstrap variables. A profile can explicitly
grant a separate Docker socket to a worker. That makes the worker
host-root-equivalent and is visible in the profile's capability warning.

Docker authority is decided at startup by whether the controller can reach a
Docker socket. Without it, no Docker launcher is registered and a session on a
Docker profile is refused. There is no fallback to a native worker, because
silently downgrading a sandboxed profile would run the model's tools in the
controller's own environment.

Events reach clients only after they are durable. A worker's turn writes through
the control plane, and the control plane publishes to the WebSocket hub after
the write lands. A client therefore never sees an event that a reconnect and
replay would not produce.

## One turn

The runtime resolves the workspace and its harness layers first. It builds the
model context from the conversation, project rules, available skill and agent
metadata, and current runtime facts. The provider may then ask to use tools.
Peen runs those tools, applies matching hooks, records the result, and returns
it to the provider until the turn completes or fails.

File mutations have a deliberate safety rule. A turn must read an existing file
before it can change that file. Peen checks the observed hash again immediately
before the mutation. New destinations must not already exist. These checks stop
stale or blind writes from silently replacing a file.

## The harness

`AGENTS.md` carries project instructions in the agents.md format, and
`.agents/rules` holds topic rules. `.agents/skills` advertises named procedures
that the model can load when needed. `.agents/agents` defines bounded child
agents. `.agents/events` tells Peen how to wake a session for an external event.
`.agents/hooks.yaml` adds mechanical actions around lifecycle and tool events.

Peen applies the configuration directory first, then filesystem layers from the
root down to the active workspace, so a layer closer to the workspace is more
specific. Layers stop at the workspace: nothing below it is read. [The
harness guide](harness.md) describes the exact rules.

## Durable state and visibility

[SQLite](https://sqlite.org/) is the sole source of truth for sessions, turns,
messages, protocol events, context and prompt snapshots, compactions,
child-agent runs and events, notices, process jobs and output, model runs, and
individual provider rounds. A model run stores the requested and returned model
identity, connection name, non-secret settings, messages, text, thinking,
usage, retries, cost, timing, and failure details. Job signal requests are
durable rows too. Each message has a direct immutable compaction link when a
summary absorbs it. Later summaries link to their parent compaction and never
rewrite that older message link, so a client can rebuild the summary tree at any
time. Structured logs go to stdout and daily audit files. The audit log records
safe identifiers and digests. The transcript holds the verbatim data, so keep
its storage and every connected client as protected as the workspace itself.

## Process lifecycle

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for process and
service lifecycle plumbing. Peen owns the agent behavior, public API, storage,
and harness. Servicepack's framework details live in its own repository.

`peen run` starts two project-owned services:

```text
control-core  ->  control-api
```

`control-core` opens validated configuration, SQLite, the provider registry,
the workspace policy, the execution profiles, and the worker supervisor. It
reports ready only after those exist, and it creates no workspace session, so a
controller starts empty.

`control-api` names control-core as its dependency, so Servicepack launches it
second and waits for control-core's readiness first. It owns the REST listener,
the global WebSocket hub, and the metrics listener, and it reports ready only
once its configured endpoint answers a connection. A client that waits for
readiness can send a request immediately instead of racing a listener that has
not bound yet.

Servicepack stops the two in reverse dependency order, so the API stops
accepting work before control-core cancels active turns, stops supervised jobs,
stops every session worker, and closes SQLite. Workers stop after jobs so a
container is not torn down under a process still running in it, and each stop is
recorded, so no generation row outlives its worker claiming to be ready.
Servicepack alone handles the stop signal and the time limit on the whole
shutdown.

The services share one `control.Core` through a handoff in
`internal/pkg/control`. A Servicepack factory takes no arguments, so it cannot
receive a shared dependency directly.

## Terms

Several words in these docs name different things on different pages. This is
what each one means.

| Term | Meaning | Where it is defined |
| --- | --- | --- |
| hook point | A fixed point in a turn where hooks run, such as `pre_write_file` or `turn_start`. The top-level keys of `hooks.yaml` name hook points. The hooks page calls them hook events, and the command stdin field `event` holds one. | [Hooks](hooks.md#events) |
| session event | A record of something that happened outside the model's own tool calls, such as `job.exited`, `agent.finished`, or a type a hook or outside caller chooses. Peen hands it to the model as data. The API calls it a notice (`/v1/session/notices`). | [Session events](events.md) |
| protocol event | A durable transcript record of one step of a turn, such as `turn.started` or `tool.use`. `GET /v1/session/events` lists them, and each one also goes out live as a WebSocket frame. | [Protocol event types](http-api.md#protocol-event-types) |
| WebSocket frame | One JSON message on the WebSocket, in either direction: a client's `message.send`, a protocol event, or a `message.completed` or `message.failed` reply. | [WebSocket](http-api.md#send-and-watch-turns-over-websocket) |
| upstream | One entry in `PEEN_UPSTREAMS`. The docs also call it a provider, and the API calls its name `connectionName`. All three mean the same configured entry. A qualified model name is `<upstream name>/<model ID>`, such as `zai/glm-5.3`. | [Providers](configuration.md#providers) |

The `agent.` prefix appears in two places. `agent.finished` and `agent.failed`
are session events. `agent.run.started`, `agent.run.tool.use`, and the other
`agent.run.*` names are protocol events of a child agent run.

The field name `type` also means different things. On a hook action it is the
action kind (`deny`, `inject`, `emit_event`, `command`). On a session event it
is the event name, which an `emit_event` action spells `event_type`. On an
upstream it is the wire protocol. On an event handler it is the session event
type the handler answers.

### Identifiers

| Identifier | What it names | Who creates it | Where you see it |
| --- | --- | --- | --- |
| session ID | One session, which is one workspace's conversation. | Peen, the first time `POST /v1/sessions/open` opens that workspace. | `X-Session-ID`, frame `metadata.sessionId`, hook stdin `sessionId`. |
| request ID | One accepted `message.send`, or one turn an event woke. | Peen, when it accepts the frame or starts the woken turn. | Frame `metadata.requestId`, turn records, hook stdin `requestId`. Every frame of one turn shares it. A queued message keeps its own. |
| turn ID | One durable turn record. | Peen, when the turn starts. | `GET /v1/session/turns`, `turnId` on stored messages, protocol events, and jobs, hook stdin `turnId`. |
| call ID | One tool call inside a turn. | The model provider. | `callId` on `tool.use` and `tool.result`, `id` and `tool_use_id` on content blocks, `toolCallId` on stored messages, hook stdin `callId`. |
| agent run ID | One `launch_agent` child run. | Peen, when the child starts. | `GET /v1/session/agents/{agentRunId}`, `runId` in `agent.*` session event data, hook stdin `agentRunId`. |
| worker generation ID | One run of a session's worker process. | Peen, when it starts the worker. | `GET /v1/session/workers`, `workerGenerationId` on turns, protocol events, and jobs. |
| HTTP request ID | One HTTP call. It has no link to the request ID above. | The client in an `X-Request-ID` request header, or Peen when the client sends none or an invalid one. | The `X-Request-ID` response header and the logs for that call. |

## External building blocks

- [Elelem](https://github.com/psyb0t/elelem) handles provider calls and tool
  rounds. [Essessey](https://github.com/psyb0t/essessey) turns its streamed
  content, tool calls, and thinking back into stable conversation blocks.
- [Aichteeteapee](https://github.com/psyb0t/aichteeteapee) runs the HTTP and
  WebSocket edge.
- [Servicepack](https://github.com/psyb0t/servicepack) owns process lifecycle.
- [Gonfiguration](https://github.com/psyb0t/gonfiguration),
  [ctxerrors](https://github.com/psyb0t/ctxerrors),
  [ctxscope](https://github.com/psyb0t/ctxscope),
  [slogging](https://github.com/psyb0t/slogging), and
  [goenv](https://github.com/psyb0t/goenv) cover configuration, errors,
  request scope, logging, and runtime environment detection.
- [GORM](https://github.com/go-gorm/gorm) persists the
  [SQLite](https://sqlite.org/) state, while
  [Prometheus' Go client](https://github.com/prometheus/client_golang) exposes
  application metrics.
