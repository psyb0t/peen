# API: live work and durable state

Use WebSocket to send messages and watch turns as they happen. Use REST to read what Peen stored or to control a run. A typed message reaches the agent only over WebSocket. The one REST call that can start a turn is [`POST /v1/session/notices`](#post-v1sessionnotices), and only when an event handler lets that notice wake an idle session. A normal socket receives live events for every session, and the client decides which session tab to render from each event's metadata.

The full REST contract is [api/api.yml](../api/api.yml) (OpenAPI 3.1). Peen's embedded browser control surface is served at `/` and is built from the same generated REST types. This page explains the parts another client needs to get right.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is required only when `PEEN_API_TOKEN` is set, as the [README](../README.md#things-worth-knowing) explains. Every response carries an `X-Request-ID` header. Every session-scoped success response, meaning any route under `/v1/session` and `/v1/messages` plus `POST /v1/sessions/open`, also carries `X-Session-ID`. Stored reads name their session with the `X-Session-ID` request header. Clients get a session ID from `POST /v1/sessions/open` or `GET /v1/sessions`. [Terms](architecture.md#terms) explains how the session ID, request ID, turn ID, and `X-Request-ID` differ. REST errors use one envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## Send and watch turns over WebSocket

Messages for the agent go only over a WebSocket upgrade at `GET /v1/ws`. The endpoint is outside the OpenAPI document, because its contract is WebSocket frames, not HTTP request and response bodies. An optional `?sessionId={canonical-uuidv4}` asks the server to send that socket only one session's frames. It does not create, select, or authorize a session. Request logging leaves out query strings.

The controller starts with no sessions. A client creates one by naming a workspace through `POST /v1/sessions/open`, the only operation that creates a session. Sessions are keyed by canonical workspace path, so opening the same directory again, by any of its names, resumes the existing session instead of making a second one. `GET /v1/sessions` lists them and takes no session header, because it is how a client finds them.

`GET /v1/workspace-roots` returns `{"roots": ["<PEEN_WORKSPACE_ROOT>"]}`, a list that always holds the one workspace root. An authenticated client may offer that root before opening a session. The embedded control surface uses it as a suggestion and still lets a user type an existing directory inside it. The endpoint does not create a session.

A workspace must be the configured `PEEN_WORKSPACE_ROOT` or sit inside it. Peen refuses a workspace outside it with `403 WORKSPACE_NOT_ALLOWED` and creates no session. It refuses a workspace that is, contains, or sits inside `PEEN_STATE_DIR`, or is or contains `PEEN_CONFIG_DIR`, the same way, because a Docker worker mounts its workspace writable. See [where it runs](where-it-runs.md#workspaces-that-would-expose-peens-own-directories). An error from `POST /v1/sessions/open` never names the root, so a caller cannot map the deployment's directory layout by probing it. A missing directory inside an allowed root returns `404 WORKSPACE_NOT_FOUND` with `workspace directory does not exist`, so a client can correct the path.

An open request may also name an agent space, `control` or `docker:<profile>`. That is the only environment choice a client makes. Images, mounts, network, and capabilities come from deployment configuration alone. A name the deployment does not offer is refused with `403 AGENT_SPACE_NOT_ALLOWED` and creates no session, even when the workspace already has one. Opening an existing session never changes the agent space it already runs in. `GET /v1/agent-spaces` lists what a client may name and the agent space a request without a name gets. Each Docker space reports its `profile`, `docker` (`none`, `readonly`, or `full`), `network` (`none`, `bridge`, or `host`), and `sudo`, plus `hostRootEquivalent` and one sentence per risky grant in `capabilityWarnings`. The `control` space reports no grants. [Where it runs](where-it-runs.md) explains the agent spaces.

`GET /v1/session/workers` reports one session's worker generations, newest first. A generation is one run of one worker process. It records the agent space and agent space revision it started under, its lifecycle state, and for a Docker generation its container and repository image digest when one is available. Turns, protocol events after the worker started, jobs, and child-agent runs record the generation that produced them. The record of an accepted user message is written before the worker starts, so it has no generation.

`POST /v1/session/reconfigure` moves an open session to a different agent space. The body carries `agentSpace` and `reason`, both required, and nothing else. Schema validation rejects an image, mount, network setting, or capability in the body before a handler sees it. An agent space the deployment does not offer is refused with `403 AGENT_SPACE_NOT_ALLOWED`, and the refusal names no agent space, so a caller cannot list the allowed set by probing. A session with a turn in flight is refused with `409 SESSION_BUSY`, because changing the environment under running work would credit that turn's tool calls to an agent space that did not run them. The same call succeeds once the turn ends. On success Peen stops the session's current worker, so the next turn starts a new generation in the new agent space.

`GET /v1/session/agent-space-decisions` reports that history, newest first. Each entry names the agent space the session moved from, the agent space it moved to, the reason the caller gave, and when it was decided.

`GET /v1/models` lists every model Peen found at controller startup. Each record gives the exact name a message uses to pick that model, its connection name, the raw model ID the upstream accepted, and the context window Peen enforces. The endpoint never makes a provider call and never returns provider credentials. Clients pass the `name` field as `data.model` in a `message.send` frame.

A `message.send` frame names its session in the event's `sessionId` metadata. That routes the message and does not authorize it. Peen loads the session before starting a turn, so an unknown session ID fails there and writes no turn record. The transport supplies the routed session, not the message body, so a message cannot redirect itself to another session. No REST endpoint accepts a user message. Peen gives each socket a server-controlled identity in its WebSocket hub, from [Aichteeteapee](https://github.com/psyb0t/aichteeteapee), then sends each accepted session's frames to all global sockets and to sockets filtered for that session. A WebSocket request that carries an `Origin` header must name the same host as the request itself, and one without an `Origin` header, which is how a client outside a browser connects, is accepted.

The accepted client event is `message.send`. Its `data` is strict JSON:

```json
{
  "id": "canonical UUIDv4 generated by the client",
  "type": "message.send",
  "data": {
    "message": "required, non-empty",
    "model": "optional provider/model override for this call only",
    "reasoningEffort": "optional minimal | low | medium | high | xhigh | max",
    "systemPrompt": {"mode": "append", "content": "optional instructions"}
  },
  "metadata": {"sessionId": "canonical UUIDv4 returned by sessions/open"},
  "timestamp": 0,
  "triggeredBy": null
}
```

`systemPrompt.mode` is `append` for the default prompt plus the supplied text, or `replace` for the supplied text alone. Both `mode` and a non-empty `content` are required when `systemPrompt` is present, and leaving either out fails the message with `VALIDATION_FAILED`. Neither setting carries over to the next message.

`reasoningEffort` sets the reasoning level for this turn's model calls. Without it the model uses its own default. Peen fits a level the model cannot take. A model without reasoning levels gets none, and a level outside the model's range runs at the nearest level it supports. The controller logs each change with the requested level, the level used, and a `reason`. Any other value fails the message with `VALIDATION_FAILED`.

`metadata.sessionId` is required on every client message. It routes the work to an existing session and does not grant access. `data.workspace` is rejected. A client learns a session ID from `POST /v1/sessions/open`, not from a special first socket frame.

Write `:skill-name` at the start of a message or after a space to hand that skill to the model. A core skill is `:core:name`. Peen checks every name before it opens a turn or contacts a provider. Each skill reaches the model as its own user message right after your message, as a `<skill>` block with the skill's name, description, directory, and full `SKILL.md`. The directory is absolute for a custom skill and `embedded://skills/<name>` for a core skill. The skill is not part of the system prompt, and a child agent only gets it when the parent names it in the `launchAgent` task. A skill that names other skills with `:name` brings them too, and Peen does not repeat a skill the model already holds, as [Skills](skills.md#once-per-conversation) explains. An unknown name produces `message.failed` with `VALIDATION_FAILED`, and nothing is stored. Without this syntax, the model sees the skill catalogue and decides whether to load a matching skill with `useSkill`. See [Skills](skills.md#forcing-a-skill).

Peen accepts the messages for one session in the order it receives them, from every connected client. The first starts a turn. Every `message.send` that arrives while that turn runs or is still starting, for example while the session's worker boots, joins the turn's user-message queue, first in first out. A queued message may repeat the running turn's `model` and `reasoningEffort`, but it cannot change them or set a `systemPrompt`, because those settings belong to the running turn, and that refusal answers `message.failed` with `VALIDATION_FAILED`. A queued message that names a skill with `:name` is resolved when Peen accepts it. An unknown name refuses that message with `VALIDATION_FAILED` and queues nothing. Otherwise the message and its skill messages are queued together, and they reach the model together. At most `PEEN_MAX_QUEUED_USER_MESSAGES` messages, 16 by default, can wait in the queue at once, counting each skill message or already-loaded note that follows a queued message, and a message that does not fit answers `USER_MESSAGE_QUEUE_FULL`. Queueing never interrupts a provider request in flight.

A queued message gets its own request ID. Peen acknowledges it with `message.completed` `{"queued": true}` and emits `userMessage.created` and then `userMessage.queued`, both carrying that message's own `requestId`. When the running turn's current model round ends, after every tool call in that round has finished, Peen hands the queued message to the model and emits `userMessage.delivered` with that same `requestId` and `{"message": "<text>"}` to every connected client. Queued messages are delivered in the order they were sent, and `userMessage.delivered` fires once per message however many skill messages follow it. Each skill message that follows is announced with a `skill.injected` frame under the running turn's `requestId`. The queue lives in the worker's memory until delivery. If the turn ends without a `userMessage.delivered` for your message's `requestId`, for example after a restart or a cancellation, the message never reached the model, so resend it.

Every accepted `message.send` first emits a stored `userMessage.created` frame. Every server frame is a JSON object with `id`, `type`, `data`, `timestamp`, `metadata`, and `triggeredBy`, the same shape as the client frame above. `triggeredBy` is the `id` of the client frame that caused this frame. A frame from a turn an event woke has no client frame behind it and carries the all-zero UUID. Nothing causes a client frame, so a client sends `null`. All session frames carry `metadata.sessionId` and `metadata.requestId`. Every frame of one turn shares the turn's `requestId`, except the `userMessage.*` frames of a queued message, which carry that message's own:

```json
{
  "id": "uuid",
  "type": "content_block_delta",
  "data": {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "Hel"}},
  "timestamp": 0,
  "metadata": {"sessionId": "uuid", "requestId": "uuid"},
  "triggeredBy": "uuid"
}
```

A turn emits Peen's own [protocol events](#protocol-event-types) plus the model's reply as Anthropic-style content blocks:

| Event | `data` | Meaning |
| --- | --- | --- |
| `content_block_start` | `{index, content_block: {type}}` | Opens block `index`. `type` is `text`, `thinking`, `tool_use` (with `id`, `name`), or `tool_result` (with `tool_use_id`, `is_error`). |
| `content_block_delta` | `{index, delta: {type, ...}}` | Appends to block `index`. `text_delta` and `thinking_delta` carry `text`, `input_json_delta` carries `partial_json`, and `json_partial` carries a tool result's `text`. |
| `content_block_stop` | `{index}` | Closes block `index`. |

Block indexes count up from zero within one turn and start over in the next, so a client keys blocks by `requestId` and `index`. A `tool_result` block answers the `tool_use` block whose `id` equals its `tool_use_id`. To render a live reply, fold the blocks of a turn in arrival order. Append `text_delta` and `thinking_delta` text to their block, and attach each tool result to its call. When `turn.completed`, `turn.failed`, or `turn.cancelled` arrives, the turn's messages are stored and `GET /v1/messages` returns them. The embedded control surface does this and then replaces the live reply with the stored messages.

A stored message with `injected: true` was added by Peen, not typed by a person or written by the model. Delivered session events are the usual case, and they arrive as a user-role message the agent reads as data. The instructions an event handler starts a turn with are injected too. A skill a user invoked with `:name` is stored as its own injected user message right after the message that named it, with `injectedKind: "skill"`, and the note that a skill is already loaded has the same kind. The typed message itself is stored as typed and is not injected. Every stored message also carries the `turnId` of the turn that wrote it, so a client can group a conversation by turn. Peen stores the rows of every turn in the order the model receives them: the prompt that started the turn, then its skill messages, then the session events delivered at the start of the turn. The prompt of a turn an event handler started is the handler's injected message.

### Protocol event types

A protocol event is a stored record of one step of a turn. Each one goes out live as a frame and is listed later by [`GET /v1/session/events`](#get-v1sessionevents). It is not a [session event](events.md). See [Terms](architecture.md#terms).

| Type | `data` | Meaning |
| --- | --- | --- |
| `userMessage.created` | `{message, sourceEventId?}` | Peen accepted a `message.send`. |
| `userMessage.queued` | `{message, sourceEventId?}` | The message joined the running turn's queue. |
| `userMessage.delivered` | `{message, sourceEventId?}` | A queued message reached the model. It carries the queued message's own `requestId`. |
| `skill.injected` | `{name, content}` | A skill message reached the model right after the user message that named it. `content` is the whole message, and `name` is the resolved skill name. The note that a skill is already loaded uses the same event. It follows `userMessage.created` at the start of a turn and `userMessage.delivered` for a queued message. |
| `harness.warning` | `{warnings: [{kind, source, reason}]}` | Peen ignored invalid optional harness files, or a skill names a skill that does not exist. See below. |
| `turn.started` | `{sessionId, model, workspace, originEventId?, originEventType?}` | The turn started. `originEventId` and `originEventType` name the session event that woke it, when one did. |
| `session.events` | `{notices, dropped}` | Session events were handed to the model. See [Session events](events.md#how-the-model-receives-events). |
| `tool.use` | `{callId, name, arguments}` | The model asked for a tool call. |
| `tool.result` | `{callId, name, content, isError}` | A tool call finished. |
| `provider.retry` | `{attempt, reason, status, delayMs}` | Peen is retrying a failed provider request. |
| `turn.completed` | `{model, text}` | The turn finished. `text` is the final answer. |
| `turn.failed` | `{reason}` | The turn failed. |
| `turn.cancelled` | `{reason}` | The turn was cancelled. |
| `agent.run.*` | `{agentRunId, parentTurnId, parentAgentRunId?, event}` | A step of a child agent run. The names are `agent.run.started`, `.text.delta`, `.thinking.delta`, `.tool.use`, `.tool.result`, `.assistant.message`, `.message.injected`, `.provider.retry`, `.completed`, `.failed`, and `.cancelled`. `event` holds the step's own payload. These frames are not in `GET /v1/session/events`. Peen stores them with the run, and `GET /v1/session/agents/{agentRunId}/events` returns them. |

`content_block_start`, `content_block_delta`, and `content_block_stop` carry the model's reply as shown above. `message.completed` and `message.failed` answer one client frame, as described below, and are not stored.

A successful submission finishes with `message.completed`. Its data is `{"queued": false}` when the turn finished, or `{"queued": true}` when the message joined a running turn's queue. `queued: true` acknowledges only that submission. The running turn hands the queued text to the model when its current model round ends, and `userMessage.delivered` reports that. `message.completed` closes the submission, not the socket. A failed submission finishes with `message.failed`, whose payload is `{code, message, reason?}`. `reason` appears only for known categories that are safe to show. Unknown provider, worker, and tool errors never expose their wrapped details.

An accepted turn may emit `harness.warning` before `turn.started`. Its data is `{"warnings":[{"kind":"skill","source":"...","reason":"..."}]}`. It means Peen ignored one or more invalid optional harness files and loaded the valid configuration around them. The event is stored and visible to every global socket for that session. A bad optional definition does not stop a turn. A `:skill-name` reference to a skill Peen ignored still fails the message.

A malformed client frame gets a private `message.failed` frame on the socket that sent it only. It creates no session and does not reach the global feed. Frames for accepted work, including later turn failures, reach every global socket and every socket filtered to that session. A filtered client does not receive other sessions, so it should use REST when it needs earlier state after switching filters.

When `PEEN_API_TOKEN` is set, clients outside a browser may send the usual `Authorization: Bearer <token>` header with the handshake. Browser clients send two WebSocket subprotocols, `peen.v1` and `peen.bearer.<base64url-token>`, where `base64url-token` is the bearer token in unpadded URL-safe Base64. Peen selects `peen.v1` and authenticates the other value. Never place a bearer token in the URL. Use `wss://` in deployment.

## POST /v1/sessions/open

Opens the session for a workspace, creating it the first time that directory is opened. No session header. `agentSpace` is optional and sets the agent space only when this call creates the session. Leave it out for the controller's default agent space. A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED`, even when the workspace already has a session.

```json
{"workspace": "/home/me/work/my-app", "agentSpace": "control"}
```

The response is the session, in the same shape as [`GET /v1/session`](#get-v1session), and whether this call created it:

```json
{"session": {"id": "uuid", "workspace": "/home/me/work/my-app", "agentSpace": "control", "...": "..."}, "created": true}
```

Use `session.id` as `metadata.sessionId` on the WebSocket and as `X-Session-ID` on REST calls.

## GET /v1/sessions

Lists every session. No session header. Query parameters are `limit` and `offset`.

```json
{"items": [{"id": "uuid", "workspace": "/home/me/work/my-app", "...": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /v1/agent-spaces

Lists the agent spaces a client may name, and the one a request without a name gets. No session header. `control` is listed only when the controller cannot reach a Docker daemon. A Docker space adds `profile`, `docker`, `network`, and `sudo`. The `control` space reports none of those.

```json
{
  "items": [
    {"name": "docker:default", "kind": "docker", "profile": "default", "revision": 0, "docker": "none", "network": "bridge", "sudo": false, "hostRootEquivalent": false, "capabilityWarnings": []},
    {"name": "docker:builder", "kind": "docker", "profile": "builder", "revision": 2, "docker": "full", "network": "bridge", "sudo": true, "hostRootEquivalent": true, "capabilityWarnings": ["docker: full mounts the host Docker socket into the worker, which is host root", "sudo: true lets the agent run commands as root inside the worker container"]}
  ],
  "default": "docker:default"
}
```

Without a Docker daemon in reach, the list starts with `{"name": "control", "kind": "control", "revision": 0, "hostRootEquivalent": false, "capabilityWarnings": []}`, and `default` is `control` unless `PEEN_DEFAULT_AGENT_SPACE` names another.

## GET /v1/messages

Lists the stored conversation messages of one existing session. `X-Session-ID` is required. Query parameters are `limit` (1 to 200, default 50), `offset` (default 0), and `order` (`asc` or `desc`, default `asc`).

```json
{
  "items": [
    {
      "id": "uuid",
      "sequence": 1,
      "role": "user",
      "content": "...",
      "workspace": "/path",
      "model": null,
      "thinking": null,
      "toolCalls": null,
      "toolCallId": null,
      "isError": false,
      "incomplete": false,
      "injected": false,
      "turnId": "uuid",
      "compactionId": null,
      "createdAt": "..."
    }
  ],
  "limit": 50,
  "offset": 0,
  "hasMore": false
}
```

`role` is `user`, `assistant`, or `tool`. Peen's internal harness records, such as context changes and resolved prompts, are not messages and never appear here.

`compactionId` names the stored compaction that summarized this message, when one did. Peen never rewrites it when a later compaction includes the earlier summary.

## GET /v1/session/turns

Lists stored turns, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. Each turn includes its request ID, workspace, state, cancellation flag, timestamps, failure category, and the hashes of the context and prompt snapshots used for that turn, when they exist.

## GET /v1/session/context-snapshots/{contextHash}

Reads the exact resolved context a turn's `contextSnapshotHash` names. `X-Session-ID` is required. The response includes the hash, creation time, resolved content, and manifest. The manifest is an array of the layers the context was built from, in resolution order, each with `kind`, `name`, `source`, `priority`, and `hash`. A hash belonging to another session returns `404`.

## GET /v1/session/prompt-snapshots/{promptHash}

Reads the exact system prompt a turn's `promptSnapshotHash` names. `X-Session-ID` is required. A hash belonging to another session returns `404`.

## GET /v1/session/compactions

Lists the session's compaction records, newest first. A compaction record never changes once written. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

Each record has the exact source message range, summary, model, prompt hash, token counts, and an optional `parentCompactionId`. The parent link forms a tree. When a later summary includes an earlier summary plus new raw messages, the later record points at the earlier one. Existing messages keep their original `compactionId`. Follow parent IDs to rebuild the whole lineage.

## GET /v1/session/compactions/{compactionId}

Reads one compaction record. `X-Session-ID` is required. The record includes the source range it covered itself and an optional `parentCompactionId`, so a client can read any point in the lineage without inferring it from message order.

## GET /v1/session/model-runs

Lists each provider call Peen recorded for the session, where one call may take several provider rounds. `X-Session-ID` is required. Query parameters are `limit`, `offset`, an optional `stage` (`turn`, `child`, or `compaction`), and an optional `state`.

A model run stores the requested and returned model identity, connection name, the settings in effect apart from secrets, text and thinking output, response messages and injections, provider usage, every cost amount, timing, and failure details. `agentRunId` names the child-agent run when a child made the call.

## GET /v1/session/model-runs/{modelRunId}/calls

Lists the exact provider rounds of one model run. `X-Session-ID` is required. Query parameters are `limit` and `offset`. The response includes the model run and each round's request messages, the tool definitions the provider saw, response message, usage, retry attempts, token categories, cost amounts, model identity, timing, and failure details. A model run ID from another session returns `404`.

## GET /v1/session

Reads one existing session. `X-Session-ID` is required.

```json
{
  "id": "uuid",
  "createdAt": "...",
  "updatedAt": "...",
  "lastMessageAt": null,
  "messageCount": 12,
  "completedTurnCount": 4,
  "activeTurn": false,
  "agent": "core:general-purpose",
  "model": "aigate/your-model-id",
  "workspace": "/home/me/work/my-app",
  "agentSpace": "control"
}
```

## POST /v1/session/cancel

Asks the session's active turn to stop. `X-Session-ID` is required, and there is no body. It returns `202` whether or not a turn was running, so repeating it is safe.

```json
{"cancelRequested": true}
```

`cancelRequested` is `true` only when this call found a running turn and signalled it. The endpoint does not wait for the turn to wind down.

## POST /v1/session/reconfigure

Moves an idle session to another agent space. `X-Session-ID` is required. Both fields are required, and any other field is rejected.

```json
{"agentSpace": "docker:builder", "reason": "needs to build container images"}
```

The response is the recorded decision:

```json
{"id": "uuid", "sessionId": "uuid", "fromAgentSpace": "docker:default", "toAgentSpace": "docker:builder", "reason": "needs to build container images", "decidedAt": "..."}
```

A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED`. A session with a turn in flight returns `409 SESSION_BUSY`. On success Peen stops the session's worker, and the next turn starts a new worker generation in the new agent space.

## GET /v1/session/agent-space-decisions

Lists one session's agent space changes, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

```json
{"items": [{"id": "uuid", "sessionId": "uuid", "fromAgentSpace": "docker:default", "toAgentSpace": "docker:builder", "reason": "needs to build container images", "decidedAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /v1/session/workers

Lists one session's worker generations, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. `containerId` and `imageDigest` appear only for a Docker generation, `imageDigest` only when the daemon reported a repository digest, and `failureDetail` only for a generation that failed or was replaced.

```json
{"items": [{"id": "uuid", "sessionId": "uuid", "kind": "docker", "agentSpace": "docker:builder", "agentSpaceRevision": 2, "state": "ready", "workspace": "/home/me/work/my-app", "containerId": "...", "imageDigest": "psyb0t/peen@sha256:...", "createdAt": "...", "startedAt": "...", "endedAt": null}], "limit": 50, "offset": 0, "hasMore": false}
```

`state` is `requested`, `starting`, `ready`, `stopping`, `stopped`, or `failed`.

## GET /v1/session/events

Lists the protocol events Peen stored while it handled the session. `X-Session-ID` is required. Query parameters are `limit`, `offset`, and `order` (`asc` or `desc`).

```json
{"events": [{"id": "uuid", "sessionId": "uuid", "turnId": "uuid", "workerGenerationId": "uuid", "sequence": 1, "requestId": "uuid", "type": "tool.use", "payload": {}, "parentToolCallId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

These are records of what happened, not a queue. Listing never consumes or changes them. The [protocol event types](#protocol-event-types) table lists every `type`.

## GET /v1/session/notices

Lists stored notices from every source in [Session events](events.md#where-events-come-from). `X-Session-ID` is required. Query parameters are `limit` and `offset`.

## POST /v1/session/notices

Records a notice from outside Peen, such as a webhook, a CI run, or an operator. `X-Session-ID` is required.

```json
{"type": "app.error", "summary": "one line", "data": {}, "delivery": "queue"}
```

`type` is a lowercase dotted name up to 128 characters. The `job.` and `agent.` prefixes are reserved for Peen's own events and are rejected here. `summary` is required and must not be empty. A `summary` longer than `PEEN_MAX_EVENT_SUMMARY_BYTES` or `data` larger than `PEEN_MAX_EVENT_DATA_BYTES` is rejected with `400 VALIDATION_FAILED`.

`delivery` is `queue`, the default, which delivers the notice at the next turn or tool boundary, or `wake`. A notice starts a turn only when a handler for its type exists in some harness layer at `<layer>/.agents/events/<type>.md`. A handler that sets `delivery` decides on its own, and a handler that leaves it out uses the notice's `delivery`. The other wake conditions, such as an idle session and the hourly wake limit, are in [waking an idle session](events.md#waking-an-idle-session). A notice that does not wake is queued. Notice content is untrusted, so Peen quotes it to the model as data and never merges it into the system prompt.

## GET /v1/session/jobs

Lists the session's process jobs, newest first. `X-Session-ID` is required. Query parameters are `limit`, `offset`, and an optional `state` filter (`running`, `exited`, `signalled`, `failed`, `interrupted`).

Each entry is the whole stored job record: `jobId`, `sessionId`, `turnId`, `pid`, `purpose`, `command`, `directory`, optional `toolCallId`, `state`, optional `workerGenerationId`, `startedAt`, optional `endedAt`, `exitCode` (`-1` while unknown), and `failureDetail`.

## GET /v1/session/jobs/{jobId}/output

Reads a window of one job's output without waiting for the job to finish. `X-Session-ID` is required. Query parameters are `stream` (`stdout`, `stderr`, or `both`, default `both`), `cursor` (default 0), and `limit` (1 to 200, default 50). The response holds ordered `lines`, each with its record ID, `sessionId`, `jobId`, a `sequence` shared by both streams, stream, content, and creation time. Use `nextCursor` to continue. With `stream=both`, the sequence keeps the order Peen saw stdout and stderr lines in. Output comes from SQLite, so a client that reconnects sees the same lines after the job has ended.

## GET /v1/session/jobs/{jobId}/signals

Lists every request to stop one job, oldest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. The response includes the whole job record plus the signal records: their IDs, session and job IDs, the requested signal, whether it reached a live process, the job's state at the time, and the timestamp.

## POST /v1/session/jobs/{jobId}/signal

Stops one job. `X-Session-ID` is required.

```json
{"signal": "stop"}
```

`stop` sends `SIGTERM` to the job's process group, then `SIGKILL` if it keeps running. `kill` sends `SIGKILL` at once. A request against an existing job that no longer has a live process is recorded and changes nothing, with `signalled: false`. An unknown job returns `404`.

## GET /v1/session/agents

Lists the session's child agent runs, newest first. `X-Session-ID` is required. Query parameters are `limit`, `offset`, and an optional `state` filter (`running`, `completed`, `failed`, `cancelled`).

Each entry is the whole stored run record: root and parent IDs, task, the instructions in effect, allowed tools, system prompt, workspace and model identity, worker generation, event count, state and cancellation flag, final text, thinking, response messages, token counts, failure details, and timestamps. `definition` is `stored` or `ad-hoc`.

## GET /v1/session/agents/{agentRunId}

Reads one stored child agent run. `X-Session-ID` is required. The record includes its parent tool call, depth, model response, token counts, failure details, and start and end times.

## GET /v1/session/agents/{agentRunId}/events

Follows one child agent run's events. `X-Session-ID` is required. Query parameters are `cursor` (default 0) and `limit` (1 to 200, default 100).

```json
{"agentRunId": "uuid", "state": "running", "events": [{"sequence": 1, "type": "...", "payload": {}, "createdAt": "..."}], "nextCursor": 1, "hasMore": false}
```

A run ID belonging to another session returns `404`, the same as an unknown one, so a caller cannot use this to probe for another session's run IDs.

## GET /v1/session/agents/{agentRunId}/messages

Lists one child agent run's own transcript, oldest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

A child agent runs in a separate model context, so its conversation lives apart from the session transcript and never appears under `GET /v1/messages`. The first record is the task its parent gave it. The rest are the assistant messages, tool results, and hook injections the child saw. Each record carries `agentRunId`, `sequence`, `role`, `content`, the `isError` and `incomplete` flags, optional `model`, `thinking`, `toolCalls`, and `toolCallId`, and `compactionId` when a child compaction covers it.

```json
{"messages": [{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "sequence": 1, "role": "user", "content": "...", "isError": false, "incomplete": false, "compactionId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /v1/session/agents/{agentRunId}/compactions

Lists one child agent run's compaction records, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

These records belong to the child. A child compaction never covers a session message and never replaces a session compaction, so this route and `GET /v1/session/compactions` describe separate lineages.

Each record has the source message range, the range it covered itself, summary, model, prompt hash, token counts, and an optional `parentCompactionId`. A later child summary covers the earlier summary plus new raw messages and points at the earlier record. The earlier record's own range and the `compactionId` on its messages stay as they were.

## GET /v1/session/agents/{agentRunId}/compactions/{compactionId}

Reads one child compaction record. `X-Session-ID` is required, and the record must belong to both the session and the agent run. A compaction ID from another session or another run returns `404`, so a caller cannot probe for records it does not own.

```json
{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "fromSequence": 1, "toSequence": 6, "directFromSequence": 3, "directToSequence": 6, "summary": "...", "sourceMessageCount": 6, "parentCompactionId": "uuid", "createdAt": "..."}
```

## POST /v1/session/agents/{agentRunId}/cancel

Cancels one child agent run without ending its parent turn. `X-Session-ID` is required, and there is no body. It behaves like `POST /v1/session/cancel` and is safe to repeat.

```json
{"agentRunId": "uuid", "cancelRequested": true, "state": "running"}
```
