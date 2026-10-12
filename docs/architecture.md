# Architecture

Peen is a durable coding-agent service. A client opens a WebSocket, sends a task for a named session, and watches the model, tools, hooks, and child agents work. The default socket is a global event feed. Clients build session tabs by filtering event metadata, or ask the server to send one session only. Peen saves the work as it happens, so reconnecting after a client or process restart keeps the conversation.

```text
WebSocket client
      |
      v
control plane: HTTP server, session hub, SQLite, worker supervisor
      |                                         ^
      | runTurn over a private Unix socket     | durable writes, then events
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

REST sits beside the WebSocket. It reads stored session state, lists messages, protocol events, notices, compactions, model exchanges, jobs, and child-agent runs, and cancels work. It never accepts a user message. It starts a turn only when a posted notice wakes an idle session through an event handler. [The API reference](http-api.md) has the contract.

[Aichteeteapee](https://github.com/psyb0t/aichteeteapee) supplies Peen's HTTP server, REST error envelope, and WebSocket hub. Peen gives every socket its own server-controlled identity in that hub. The server sends a session's events to global sockets and to sockets filtered for that session.

## Control plane and workers

One host runs one control plane. It owns SQLite, the REST listener, the global WebSocket hub, session routing, and worker lifecycle. It does not run the model loop. There are no controller IDs and no second controller.

Each session's turns run in a worker process the control plane starts when the session first needs one. A worker never opens the control plane's SQLite file. It reaches stored state through the control plane over a private Unix socket for its session, which carries JSON frames to run a turn, cancel it, signal a job, and shut down. Only the controller's own user can open that socket, and the worker authenticates with a credential made for that one worker. The control plane stores only the SHA-256 hash of the credential, hands the raw value to the worker on stdin, and never writes it to a command line, an environment variable, a label, or a log.

Each session runs in an agent space the deployment offers, and the client names the agent space and nothing else. Images, mounts, network, the Docker socket, and sudo come from deployment configuration. The `control` agent space starts the same installed Peen binary with its internal `worker` command, as a child of the controller. A `docker:<profile>` agent space creates a container named `peen-worker-<session-uuid>` with two labels, `peen.managed=true` and `peen.session=<session-uuid>`, and the supervisor acts on a stored container ID only while both labels still match. [Where it runs](where-it-runs.md) is the reference for agent spaces and Docker profiles.

A Docker worker runs as the controller's own host UID, GID, and username. A controller on the host reads that account from the operating system. A controller running in Docker with numeric `--user` IDs reads `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`, so a worker can recreate the same host account. Every Docker worker container starts as root only long enough for the image entrypoint to create that account and drop to it. A profile that sets `sudo: true` also gets a passwordless sudo rule for that account. The agent is the host user either way. See [sudo](configuration.md#sudo).

The controller passes each Docker worker the runtime configuration it needs to run a turn, including provider definitions and named provider credentials. It never passes the controller's SQLite state, the API token, the controller's Docker socket, or the entrypoint's account settings. A Docker profile can grant a worker its own Docker socket with `docker: full`. That makes the worker host root, and the agent space's capability warnings say so.

A profile with `docker: readonly` gets no socket. For each such worker the controller runs a Docker API proxy on a Unix socket in that session's own directory, which the worker already mounts, and sets the worker's `DOCKER_HOST` to it. The proxy forwards only a fixed list of read requests to the controller's own daemon socket, hides environment values in inspect output, and stops when the worker stops. [`docker: readonly`](where-it-runs.md#docker-readonly) lists what it answers.

The controller decides at startup whether it has Docker access, by checking whether it can reach a Docker socket. Without one, it registers no way to start containers, and a session in a Docker agent space is refused. Peen never falls back to the `control` agent space, because quietly giving up a container would run the model's tools in the controller's own environment. With a socket the rule runs the other way, and `control` is never offered, because a control worker could use the controller's socket to become host root.

Events reach clients only after they are stored. A worker's turn writes through the control plane, and the control plane publishes to the WebSocket hub after the write lands. A client therefore never sees an event that a reconnect and replay would not produce.

## One turn

The runtime recovers the root agent's current directory and resolves its ancestor harness layers first. It builds the model context from the conversation, project rules, the names of the available skills and agents, and current runtime facts. The provider may then ask to use tools. Peen runs those tools, applies matching hooks, records the result, and returns it to the provider until the turn completes or fails.

A replacement worker restores interrupted child conversations before serving session work. Each child keeps its run identity and recorded progress; a replaced worker cannot write new child checkpoints after ownership transfers. [Child recovery](agents.md#recovery-after-a-crash) describes pending tools, messages and limits.

File changes follow one safety rule. A turn must read an existing file before it can change that file, and Peen checks the file's hash again right before the change. A new file must not exist yet. These checks stop a write based on an old or unread copy from replacing a file.

## The harness

`AGENTS.md` carries project instructions in the agents.md format, and `.agents/rules` holds topic rules. `.agents/skills` lists named procedures that the model can load when needed. `.agents/agents` defines child agents for bounded jobs. `.agents/events` tells Peen how to wake a session for an outside event. `.agents/hooks.yaml` adds mechanical actions around turn and tool events.

Peen applies the configuration directory first, then filesystem layers from the root down to the agent's current directory. A `changeDirectory` call reloads every harness piece for that directory and its ancestors. Child agents change independently. [The harness guide](harness.md) describes the exact rules.

## Durable state and visibility

[SQLite](https://sqlite.org/) is the only stored record of sessions, turns, messages, protocol events, context and prompt snapshots, compactions, child-agent runs and events, notices, process jobs and output, model runs, and individual provider rounds. A model run stores the requested and returned model identity, connection name, settings apart from secrets, messages, text, thinking, usage, retries, cost, timing, and failure details. Job signal requests are stored too. A message that a summary absorbs gets a link to that compaction, and the link never changes. A later summary links to its parent compaction instead of rewriting that older link, so a client can rebuild the summary tree at any time.

Structured logs go to stdout and to daily audit files. The audit log records identifiers and digests that are safe to keep. The transcript holds the verbatim data, so protect its storage and every connected client as well as the workspace itself.

## Process lifecycle

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for process and service lifecycle. Peen owns the agent behavior, public API, storage, and harness. Servicepack's own details live in its repository.

`peen run` starts two services:

```text
control-core  ->  control-api
```

`control-core` loads and checks the configuration, opens SQLite, and builds the provider registry, the workspace policy, the agent spaces, and the worker supervisor. It reports ready only after those exist, and it creates no session, so a controller starts empty.

`control-api` depends on `control-core`, so Servicepack starts it second and waits for `control-core` to be ready first. It owns the REST listener, the global WebSocket hub, and the metrics listener, and it reports ready only once its endpoint accepts a connection. A client that waits for readiness can send a request right away instead of racing a listener that has not started yet.

Servicepack stops the two in reverse order, so the API stops accepting work before `control-core` cancels active turns, stops running jobs, stops every session worker, and closes SQLite. Workers stop after jobs, so a container is not removed while a process still runs in it, and Peen records each stop, so no worker generation stays marked ready after its worker is gone. Servicepack handles the stop signal and the time limit on the whole shutdown.

## In-process embedding

The [Go embedding guide](../pkg/peen/README.md) describes running the same engine inside another process. That runtime owns its SQLite connection, jobs and child goroutines directly, and its caller closes it with Runtime.Close. Session handles expose stored replay and controls without starting the controller or worker transports. A supplied filesystem affects workspace tools, prompts and harness discovery; configuration, state and audit logs remain in separate host directories. Caller-provided logging and metrics stay local to that runtime.

## Terms

Several words in these docs name different things on different pages. This is what each one means.

| Term | Meaning | Where it is defined |
| --- | --- | --- |
| hook point | A fixed point in a turn where hooks run, such as `preWriteFile` or `turnStart`. The top-level keys of `hooks.yaml` name hook points. The hooks page calls them hook events, and the command stdin field `event` holds one. | [Hooks](hooks.md#events) |
| session event | A record of something that happened outside the model's own tool calls, such as `job.exited`, `agent.finished`, or a type a hook or outside caller chooses. Peen hands it to the model as data. The API calls it a notice (`/v1/session/notices`). | [Session events](events.md) |
| protocol event | A stored record of one step of a turn, such as `turn.started` or `tool.use`. `GET /v1/session/events` lists them, and each one also goes out live as a WebSocket frame. | [Protocol event types](http-api.md#protocol-event-types) |
| WebSocket frame | One JSON message on the WebSocket, in either direction: a client's `message.send`, a protocol event, or a `message.completed` or `message.failed` reply. | [WebSocket](http-api.md#send-and-watch-turns-over-websocket) |
| upstream | One entry in `PEEN_UPSTREAMS`. The docs also call it a provider, and the API calls its name `connectionName`. All three mean the same configured entry. A qualified model name is `<upstream name>/<model ID>`, such as `zai/glm-5.3`. | [Providers](configuration.md#providers) |
| agent space | The place a workspace's agent runs: `control`, next to Peen, or `docker:<profile>`, in its own container. A Docker profile is one entry in `PEEN_DOCKER_PROFILES`. | [Where it runs](where-it-runs.md) |
| control space | Where Peen itself runs when you install it with the installer: `host` or `docker`. | [Where it runs](where-it-runs.md#the-four-combinations) |
| worker generation | One run of a session's worker process. A session gets a new generation each time Peen starts its worker. | [Workers](http-api.md#get-v1sessionworkers) |

The `agent.` prefix appears in two places. `agent.finished`, `agent.failed`, `agent.message`, and `agent.question` are session events. `agent.run.started`, `agent.run.tool.use`, and the other `agent.run.*` names are protocol events of a child agent run.

A child agent runs in its session's worker, on its own, and can outlive the turn that launched it. It talks to the agent that launched it through messages and session events. See [running children](agents.md#running-children).

The field name `type` also means different things. On a hook action it is the action kind (`deny`, `inject`, `emitEvent`, `command`). On a session event it is the event name, which an `emitEvent` action spells `eventType`. On an upstream it is the wire protocol. On an event handler it is the session event type the handler answers.

### Identifiers

| Identifier | What it names | Who creates it | Where you see it |
| --- | --- | --- | --- |
| session ID | One session, which is one workspace's conversation. | Peen, the first time `POST /v1/sessions/open` opens that workspace. | `X-Session-ID`, frame `metadata.sessionId`, hook stdin `sessionId`. |
| request ID | One accepted `message.send`, or one turn an event woke. | Peen, when it accepts the frame or starts the woken turn. | Frame `metadata.requestId`, turn records, hook stdin `requestId`. Every frame of one turn shares it. A queued message keeps its own. |
| turn ID | One stored turn. | Peen, when the turn starts. | `GET /v1/session/turns`, `turnId` on stored messages, protocol events, and jobs, hook stdin `turnId`. |
| call ID | One tool call inside a turn. | The model provider. | `callId` on `tool.use` and `tool.result`, `id` and `tool_use_id` on content blocks, `toolCallId` on stored messages, hook stdin `callId`. |
| agent run ID | One `launchAgent` child run. | Peen, when the child starts. | `GET /v1/session/agents/{agentRunId}`, `runId` in `agent.*` session event data, hook stdin `agentRunId`. |
| worker generation ID | One run of a session's worker process. | Peen, when it starts the worker. | `GET /v1/session/workers`, `workerGenerationId` on turns, protocol events, and jobs. |
| HTTP request ID | One HTTP call. It has no link to the request ID above. | The client in an `X-Request-ID` request header, or Peen when the client sends none or an invalid one. | The `X-Request-ID` response header and the logs for that call. |

## External building blocks

- [Elelem](https://github.com/psyb0t/elelem) handles provider calls and tool rounds. [Essessey](https://github.com/psyb0t/essessey) turns its streamed content, tool calls, and thinking back into stable conversation blocks.
- [Aichteeteapee](https://github.com/psyb0t/aichteeteapee) runs the HTTP and WebSocket edge.
- [Servicepack](https://github.com/psyb0t/servicepack) owns process lifecycle.
- [Gonfiguration](https://github.com/psyb0t/gonfiguration), [ctxerrors](https://github.com/psyb0t/ctxerrors), [ctxscope](https://github.com/psyb0t/ctxscope), [slogging](https://github.com/psyb0t/slogging), and [goenv](https://github.com/psyb0t/goenv) cover configuration, errors, request scope, logging, and runtime environment detection.
- [GORM](https://github.com/go-gorm/gorm) stores the [SQLite](https://sqlite.org/) state, and [Prometheus' Go client](https://github.com/prometheus/client_golang) serves the application metrics.
