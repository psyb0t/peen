# API: live work and durable state

Use WebSocket to send messages and watch turns as they happen. Use REST to read
what Peen stored or to control a run. REST never starts a turn. A normal socket
receives live events for every session. The client decides which session tab to
render from each event's metadata.

The full REST contract is [api/api.yml](../api/api.yml) (OpenAPI 3.1). Peen's embedded browser control surface is served at `/` and is built from the same generated REST types. This page explains the parts another client needs to get right.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is
required only when `PEEN_API_TOKEN` is set; see the root
[README](../README.md#things-worth-knowing). Every response carries an
`X-Request-ID` header. Every session-scoped success response (any route under
`/v1/session` and `/v1/messages`, plus `POST /v1/sessions/open`) also carries
`X-Session-ID`. Durable reads are addressed to a known session through
`X-Session-ID`. Clients get a session ID from `POST /v1/sessions/open` or
`GET /v1/sessions`. [Terms](architecture.md#terms) explains how the session
ID, request ID, turn ID, and `X-Request-ID` differ. REST errors use one
envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## Send and watch turns over WebSocket

Agent messages are submitted only over a WebSocket upgrade at `GET /v1/ws`.
The endpoint is outside the OpenAPI document because its contract is WebSocket
frames, not HTTP request and response bodies. An optional
`?sessionId={canonical-uuidv4}` requests a server-side outbound filter for that
one session. It does not create, select, or authorize a session. Request
logging excludes query strings.

The control service starts with no sessions. A client creates one by naming a
workspace through `POST /v1/sessions/open`, the only operation that creates a
session. Sessions are keyed by canonical workspace path, so opening the same
directory again, by any of its names, resumes the existing session instead of
making a second one. `GET /v1/sessions` lists them, and takes no session header
because it is how a client discovers them.

`GET /v1/workspace-roots` returns `{"roots": ["<PEEN_WORKSPACE_ROOT>"]}`,
which today always holds one entry. An authenticated client may offer that
root before opening a session. The embedded control surface uses it as a
suggestion, while still allowing a user to type an existing child directory.
The endpoint does not create a session.

A workspace must be the configured `PEEN_WORKSPACE_ROOT` or sit inside it.
Peen refuses a workspace outside it with `403 WORKSPACE_NOT_ALLOWED` and
creates no session. An error from `POST /v1/sessions/open` never names the
root, so a caller cannot map the deployment's directory layout by probing it. A missing directory beneath an allowed root
returns `404 WORKSPACE_NOT_FOUND` with `workspace directory does not exist`, so
a client can correct the path instead of receiving a generic server failure.

An open request may also name an execution profile. That is the only environment
choice a client makes: images, mounts, network, and capabilities come from
deployment configuration alone. A name the operator did not define is refused
and creates no session, and opening an existing session never changes the
profile it already runs under. `GET /v1/execution-profiles` lists what a client
may name, with `hostRootEquivalent` and a `capabilityWarning` for a profile that
grants effectively host-root access.

`GET /v1/session/workers` reports one session's durable worker generations, newest first. A generation is one run of one worker process. It records the profile and profile revision it started under, its lifecycle state, and for a Docker generation its container and repository image digest when one is available. Turns, post-start protocol events, jobs, and child-agent runs record the generation that produced them. The accepted user-message record predates worker startup, so it has no generation.

`POST /v1/session/reconfigure` moves an already-open session to a different
profile. The body carries `profile` and `reason`, both required, and nothing
else. An image, mount, network setting, or capability in the body is rejected by
schema validation before a handler sees it. An undefined profile is refused with
`403 EXECUTION_PROFILE_NOT_ALLOWED`, and the refusal names no profile, so a
caller cannot enumerate the allowed set by probing. A session with a turn in
flight is refused with `409 SESSION_BUSY`, because changing the environment
under running work would attribute that turn's tool calls to a profile that did
not run them. The same call succeeds once the turn ends. On success Peen stops
the session's current worker, so the next turn starts a new generation under
the new profile instead of continuing in the old one.

`GET /v1/session/profile-decisions` reports that history, newest first. Each
entry names the profile the session moved from, the profile it moved to, the
reason the caller gave, and when it was decided.

`GET /v1/models` lists every model Peen discovered at controller startup. Each record provides the exact one-turn override name, its configured connection name, the raw model ID the upstream accepted, and Peen's enforced context window. The endpoint never makes a provider call and never returns provider credentials. Clients pass the `name` field as `data.model` in a `message.send` frame.

A `message.send` frame names its session in the event's `sessionId` metadata.
That routes the message, it does not authorize it. Peen loads the session
before starting a turn, so an unknown session ID fails there and writes no turn
record. The transport supplies the routed session, not the message body, so a
message cannot redirect itself to another session. No REST endpoint accepts a
user message. Peen gives each socket a server-controlled identity in its
WebSocket hub (from [Aichteeteapee](https://github.com/psyb0t/aichteeteapee)),
then fans each accepted session's frames to all global sockets and to sockets
filtered for that session. The server keeps the hub's default origin policy: an `Origin`
header must match the request host outside explicitly enabled local development
mode.

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

`systemPrompt.mode` is `append` for the default prompt plus the supplied text,
or `replace` for the supplied text alone. Both `mode` and a non-empty
`content` are required when `systemPrompt` is present. Leaving either out fails
the message with `VALIDATION_FAILED`. Neither setting is sticky.
`reasoningEffort` sets the reasoning level for this turn's model calls. Without it the model uses its own default. A level the model cannot take is fitted to it: a model without reasoning levels gets none, and a level outside the model's range runs at the nearest level it supports. The controller logs each change with the requested level, the level used, and a `reason`. Any other value fails the message with `VALIDATION_FAILED`.
`metadata.sessionId` is required on every client message. It routes the work to an existing session and is not an authorization grant. `data.workspace` is rejected. A client learns a session ID from `POST /v1/sessions/open`, not from a special first socket frame.

Write a standalone `:skill-name` at the start of a message or after whitespace
to require that exact resolved skill for the turn. Peen validates the name
before it opens a turn or contacts a provider. The full `SKILL.md` reaches the
root and any child agents. An unknown name produces `message.failed`; the user
message remains unchanged. Without this syntax, the model sees the skill
catalogue and decides whether to load a matching procedure with `use_skill`.

Peen admits the messages for one session in the order it receives them, from every connected client. The first starts a turn, and every `message.send` that arrives while that turn runs or is still starting, for example while the session's worker boots, joins the turn's FIFO user-message queue. A queued message may repeat the running turn's `model` and `reasoningEffort`, but it cannot change them or set a `systemPrompt`, because those settings belong to the running turn, and that refusal answers `message.failed` with `VALIDATION_FAILED`. A queued message that names a skill with `:name` is queued as written. The running prompt is fixed, so the skill is not forced; the model sees the reference and loads the skill with `use_skill`. The live queue holds at most `PEEN_MAX_QUEUED_USER_MESSAGES` messages, 16 by default, and a message past that answers `USER_MESSAGE_QUEUE_FULL`. Queueing never interrupts an in-flight provider request.

A queued message gets its own request ID. Peen acknowledges it with `message.completed` `{"queued": true}` and emits `user_message.created` and then `user_message.queued`, both carrying that message's own `requestId`. When the running turn's current model round ends, after the current tool call finishes, Peen hands the queued message to the model and emits `user_message.delivered` with that same `requestId` and `{"message": "<text>"}` to every connected client. Queued messages are delivered in the order they were sent. The queue lives in the worker's memory until delivery. If the turn ends without a `user_message.delivered` for your message's `requestId`, for example after a restart or a cancellation, the message never reached the model, so resend it.

Every accepted `message.send` first emits a durable `user_message.created`
frame. Every server frame is a JSON object with `id`, `type`, `data`,
`timestamp`, `metadata`, and `triggeredBy`, the same shape as the client frame
above. `triggeredBy` is the `id` of the client frame that caused this frame.
A frame from a turn an event woke has no client frame behind it and carries the
all-zero UUID. A client frame is caused by nothing, so a client sends `null`.
All session frames carry `metadata.sessionId` and `metadata.requestId`. Every frame of one turn shares the turn's `requestId`, except the `user_message.*` frames of a queued message, which carry that message's own:

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
| `content_block_delta` | `{index, delta: {type, ...}}` | Appends to block `index`: `text_delta` and `thinking_delta` carry `text`, `input_json_delta` carries `partial_json`, and `json_partial` carries a tool result's `text`. |
| `content_block_stop` | `{index}` | Closes block `index`. |

Block indexes count up from zero within one turn and start over in the next, so a client keys blocks by `requestId` and `index`. A `tool_result` block answers the `tool_use` block whose `id` equals its `tool_use_id`. To render a live reply, fold the blocks of a turn in arrival order: append `text_delta` and `thinking_delta` text to their block, and attach each tool result to its call. When `turn.completed`, `turn.failed`, or `turn.cancelled` arrives, the turn's messages are durable and `GET /v1/messages` returns them. The embedded control surface does exactly this and then replaces the live reply with the stored messages.

A stored message with `injected: true` was added by Peen, not typed by a person or written by the model. Delivered session events are the common case: they arrive as a user-role message the agent reads as data. The instructions an event handler starts a turn with are injected too. Every stored message also carries the `turnId` of the turn that wrote it, so a client can group a conversation by turn. Session events delivered at the start of a turn are stored ahead of that turn's prompt.

### Protocol event types

A protocol event is a durable record of one step of a turn. Each one goes out live as a frame and is listed later by [`GET /v1/session/events`](#get-v1sessionevents). It is not a [session event](events.md). See [Terms](architecture.md#terms).

| Type | `data` | Meaning |
| --- | --- | --- |
| `user_message.created` | `{message, sourceEventId?}` | Peen accepted a `message.send`. |
| `user_message.queued` | `{message, sourceEventId?}` | The message joined the running turn's queue. |
| `user_message.delivered` | `{message, sourceEventId?}` | A queued message reached the model. It carries the queued message's own `requestId`. |
| `harness.warning` | `{warnings: [{kind, source, reason}]}` | Peen ignored invalid optional harness files. See below. |
| `turn.started` | `{sessionId, model, workspace, originEventId?, originEventType?}` | The turn started. `originEventId` and `originEventType` name the session event that woke it, when one did. |
| `session.events` | `{notices, dropped}` | Session events were handed to the model. See [Session events](events.md#how-the-model-receives-events). |
| `tool.use` | `{callId, name, arguments}` | The model asked for a tool call. |
| `tool.result` | `{callId, name, content, isError}` | A tool call finished. |
| `provider.retry` | `{attempt, reason, status, delayMs}` | Peen is retrying a failed provider request. |
| `turn.completed` | `{model, text}` | The turn finished. `text` is the final answer. |
| `turn.failed` | `{reason}` | The turn failed. |
| `turn.cancelled` | `{reason}` | The turn was cancelled. |
| `agent.run.*` | `{agentRunId, parentTurnId, parentAgentRunId?, event}` | A step of a child agent run. The names are `agent.run.started`, `.text.delta`, `.thinking.delta`, `.tool.use`, `.tool.result`, `.assistant.message`, `.message.injected`, `.provider.retry`, `.completed`, `.failed`, and `.cancelled`. `event` holds the step's own payload. These frames are not in `GET /v1/session/events`. Peen stores them with the run, and `GET /v1/session/agents/{agentRunId}/events` returns them. |

`content_block_start`, `content_block_delta`, and `content_block_stop` carry the model's reply as shown above. `message.completed` and `message.failed` are replies to one client frame, described below, and are not stored.

Successful submissions finish with `message.completed`. Its data is
`{"queued": false}` when the turn finished or `{"queued": true}` when the
message joined an active turn's queue. `queued: true` acknowledges only that
submission. The running turn hands the queued text to the model when its
current model round ends, and `user_message.delivered` reports that.
`message.completed` closes the submission, not the socket.
An unsuccessful submission finishes with `message.failed`. Its safe payload is
`{code, message, reason?}`. `reason` appears only for known safe categories.
Unknown provider, worker, and tool errors never expose their wrapped details.

An accepted turn may emit `harness.warning` before `turn.started`. Its data is
`{"warnings":[{"kind":"skill","source":"...","reason":"..."}]}`.
This means Peen ignored one or more invalid optional harness sources and loaded
the valid configuration around them. The event is durable and visible to every
global socket for that session. A bad optional definition does not stop a turn;
an explicit `:skill-name` reference to an ignored skill still fails clearly.

Malformed client commands receive a private `message.failed` frame only on the
originating socket. They create no session and are not added to the global
feed. Frames for accepted work, including later turn failures, are visible to
every global socket and to matching filtered sockets. A filtered client does
not receive unrelated sessions, so it should use REST when it needs historical
state after switching filters.

When `PEEN_API_TOKEN` is set, non-browser clients may use the normal
`Authorization: Bearer <token>` handshake header. Browser clients send two
WebSocket subprotocols: `peen.v1` and
`peen.bearer.<base64url-token>`, where `base64url-token` is the bearer token's
unpadded URL-safe Base64 form. Peen selects `peen.v1` and authenticates the
other value. Never place a bearer token in the URL. Use `wss://` in deployment.

## POST /v1/sessions/open

Opens the session for a workspace, creating it the first time that directory is opened. No session header. `profile` is optional and applies only when this call creates the session.

```json
{"workspace": "/home/me/work/my-app", "profile": "native"}
```

The response is the session, in the same shape as [`GET /v1/session`](#get-v1session), and whether this call created it:

```json
{"session": {"id": "uuid", "workspace": "/home/me/work/my-app", "executionProfile": "native", "...": "..."}, "created": true}
```

Use `session.id` as `metadata.sessionId` on the WebSocket and as `X-Session-ID` on REST calls.

## GET /v1/sessions

Lists every session. No session header. Query parameters are `limit` and `offset`.

```json
{"items": [{"id": "uuid", "workspace": "/home/me/work/my-app", "...": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /v1/messages

Lists stored conversation messages for one existing session.
`X-Session-ID` is required. Query parameters: `limit` (1-200, default 50),
`offset` (default 0), `order` (`asc` or `desc`, default `asc`).

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

`role` is `user`, `assistant`, or `tool`. Internal harness records (context
changes, resolved prompts) are not messages and never appear here.

`compactionId` is present when that message is directly represented by a
stored compaction. It is never rewritten when a later compaction includes the
earlier summary.

## GET /v1/session/turns

Lists durable turn lifecycles, newest first. `X-Session-ID` is required.
Query parameters are `limit` and `offset`. Each turn includes its request ID,
workspace, state, cancellation flag, timestamps, failure classification, and
the optional context and prompt snapshot hashes used for that turn.

## GET /v1/session/context-snapshots/{contextHash}

Reads the exact resolved context identified by a turn's `contextSnapshotHash`.
`X-Session-ID` is required. The response includes the hash, creation time,
resolved content, and manifest. The manifest is an array of the layers the
context was assembled from, in resolution order, each carrying `kind`, `name`,
`source`, `priority`, and `hash`. A hash belonging to another session returns
`404`.

## GET /v1/session/prompt-snapshots/{promptHash}

Reads the exact effective system prompt identified by a turn's
`promptSnapshotHash`. `X-Session-ID` is required. A hash belonging to another
session returns `404`.

## GET /v1/session/compactions

Lists immutable compaction records, newest first. `X-Session-ID` is required.
Query parameters are `limit` and `offset`.

Each record has the exact source message range, summary, model, prompt hash,
token counts, and optional `parentCompactionId`. The parent link forms a tree:
when a later summary includes an earlier summary plus new raw messages, the
later record points at the earlier one. Existing messages keep their original
`compactionId`. Follow parent IDs to rebuild the whole lineage.

## GET /v1/session/compactions/{compactionId}

Reads one immutable compaction record. `X-Session-ID` is required. The record
includes its direct source range and optional `parentCompactionId`, so a client
can retrieve any point in the lineage without inferring it from message order.

## GET /v1/session/model-runs

Lists each logical provider invocation recorded for the session. `X-Session-ID`
is required. Query parameters are `limit`, `offset`, optional `stage`
(`turn`, `child`, or `compaction`), and optional terminal or running `state`.

A model run stores the requested and returned model identity, connection name,
effective non-secret settings, text and thinking output, response messages and
injections, provider usage, every cost amount, timing, and failure details.
`agentRunId` identifies the child-agent run when a child made the call.

## GET /v1/session/model-runs/{modelRunId}/calls

Lists the exact provider rounds belonging to one model run. `X-Session-ID` is
required. Query parameters are `limit` and `offset`. The response includes the
owning model run and each round's request messages, provider-visible tool
definitions, response message, usage, retry attempts, token categories, cost
amounts, model identity, timing, and failure details. A model run ID from
another session returns `404`.

## GET /v1/session

Reads details for one existing session. `X-Session-ID` is required.

```json
{
  "id": "uuid",
  "createdAt": "...",
  "updatedAt": "...",
  "lastMessageAt": null,
  "messageCount": 12,
  "completedTurnCount": 4,
  "activeTurn": false,
  "agent": "default",
  "model": "aigate/your-model-id",
  "workspace": "/home/me/work/my-app",
  "executionProfile": "native"
}
```

## POST /v1/session/cancel

Requests cancellation of the session's active turn. `X-Session-ID` is
required, no body. Idempotent: returns `202` whether a turn was actually
running or not.

```json
{"cancelRequested": true}
```

`cancelRequested` is `true` only when this call found a running turn and
signalled it. The endpoint does not wait for the turn to unwind.

## GET /v1/session/events

Lists durable protocol events recorded while Peen handled the session.
`X-Session-ID` is required. Query parameters are `limit`, `offset`, and
`order` (`asc` or `desc`).

```json
{"events": [{"id": "uuid", "sessionId": "uuid", "turnId": "uuid", "workerGenerationId": "uuid", "sequence": 1, "requestId": "uuid", "type": "tool.use", "payload": {}, "parentToolCallId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

These are transcript protocol records, not a queue. Listing never consumes or
alters them. The [protocol event types](#protocol-event-types) table lists
every `type`.

## GET /v1/session/notices

Lists durable notices from any source in
[Session events](events.md#where-events-come-from). `X-Session-ID` is required. Query parameters are `limit` and `offset`.

## POST /v1/session/notices

Records a notice from outside Peen, such as a webhook, CI run, or operator.
`X-Session-ID` is required.

```json
{"type": "app.error", "summary": "one line", "data": {}, "delivery": "queue"}
```

`type` is a lowercase dotted name up to 128 characters. The `job.` and
`agent.` prefixes are reserved for Peen's own producers and are rejected here.
`summary` is required and must not be empty. A `summary` longer than
`PEEN_MAX_EVENT_SUMMARY_BYTES` or `data` larger than `PEEN_MAX_EVENT_DATA_BYTES`
is rejected with `400 VALIDATION_FAILED`. `delivery` is `queue` (default,
delivered at the next turn or tool boundary) or `wake`. A notice starts a turn
only when a handler for its type exists in some harness layer at
`<layer>/.agents/events/<type>.md`. A handler that sets `delivery` decides on
its own. A handler that leaves it out uses the notice's `delivery`. The other
wake conditions, such as an idle session and the hourly wake cap, are in
[waking an idle session](events.md#waking-an-idle-session). A notice that does
not wake is queued. Notice content is untrusted. Peen quotes it as data for the model and never
merges it into the system prompt.

## GET /v1/session/jobs

Lists the session's process jobs, newest first. `X-Session-ID` is required.
Query parameters: `limit`, `offset`, and an optional `state` filter
(`running`, `exited`, `signalled`, `failed`, `interrupted`).

Each entry is the full durable job row: `jobId`, `sessionId`, `turnId`, `pid`,
`purpose`, `command`, `directory`, optional `toolCallId`, `state`,
optional `workerGenerationId`, `startedAt`, optional `endedAt`, `exitCode`
(`-1` while unknown), and `failureDetail`. The live ring buffers are not this
API's source of truth.

## GET /v1/session/jobs/{jobId}/output

Reads a bounded window of one job's output without waiting for it to finish.
`X-Session-ID` is required. Query parameters: `stream` (`stdout`, `stderr`,
or `both`, default `both`), `cursor` (default 0), and `limit` (1-200, default
50). The response contains ordered immutable `lines`; each has its database
row ID, `sessionId`, `jobId`, shared `sequence`, stream, content, and creation
time. Use `nextCursor` to continue. With `stream=both`, sequence preserves the
observed stdout and stderr ordering. Output comes from SQLite, so reconnecting
clients see the same lines even after the live ring buffer is gone.

## GET /v1/session/jobs/{jobId}/signals

Lists every request to stop one job, oldest first. `X-Session-ID` is required.
Query parameters are `limit` and `offset`. The response includes the full job
row plus immutable signal records: their IDs, session and job IDs, requested
signal, whether it reached a live process, state at the time, and timestamp.

## POST /v1/session/jobs/{jobId}/signal

Stops one job. `X-Session-ID` is required.

```json
{"signal": "stop"}
```

`stop` sends `SIGTERM` to the job's process group, then escalates to
`SIGKILL`; `kill` goes straight to `SIGKILL`. A request against an existing
job that no longer has a live process is a durable no-op with
`signalled: false`. An unknown job returns `404`.

## GET /v1/session/agents

Lists the session's child agent runs, newest first. `X-Session-ID` is
required. Query parameters: `limit`, `offset`, and an optional `state` filter
(`running`, `completed`, `failed`, `cancelled`).

Each entry is a full durable run record: root and parent IDs, task, effective
instructions, allowed tools, system prompt, workspace and model identity,
worker generation, event count, state and cancellation flag, final text,
thinking, response messages, token counts, failure details, and timestamps.
`definition` is `stored` or `ad-hoc`.

## GET /v1/session/agents/{agentRunId}

Reads one durable child agent run. `X-Session-ID` is required. The record
includes its parent tool call, depth, model response, token counts, failure
details, and start and end times.

## GET /v1/session/agents/{agentRunId}/events

Follows one child agent run's events. `X-Session-ID` is required. Query
parameters: `cursor` (default 0) and `limit` (1-200, default 100).

```json
{"agentRunId": "uuid", "state": "running", "events": [{"sequence": 1, "type": "...", "payload": {}, "createdAt": "..."}], "nextCursor": 1, "hasMore": false}
```

A run ID belonging to another session returns `404`, the same as an unknown
one, so a caller cannot use this to probe for another session's run IDs.

## GET /v1/session/agents/{agentRunId}/messages

Lists one child agent run's own transcript, oldest first. `X-Session-ID` is
required. Query parameters are `limit` and `offset`.

A child agent runs a separate model context, so its conversation lives apart
from the session transcript and never appears under `GET /v1/messages`. The
first record is the task its parent gave it. The rest are the assistant
messages, tool results, and hook injections the child saw. Each record carries
`agentRunId`, `sequence`, `role`, `content`, the `isError` and `incomplete`
flags, optional `model`, `thinking`, `toolCalls`, and `toolCallId`, and
`compactionId` when a child compaction covers it.

```json
{"messages": [{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "sequence": 1, "role": "user", "content": "...", "isError": false, "incomplete": false, "compactionId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /v1/session/agents/{agentRunId}/compactions

Lists one child agent run's compaction records, newest first. `X-Session-ID` is
required. Query parameters are `limit` and `offset`.

These records are the child's own. A child compaction never covers a session
message and never supersedes a session compaction, so this route and
`GET /v1/session/compactions` describe separate lineages.

Each record has the source message range, the direct range it covered itself,
summary, model, prompt hash, token counts, and optional `parentCompactionId`. A
later child summary covers the earlier summary plus new raw messages and points
at the earlier record. The earlier record's direct range and the `compactionId`
on its messages stay as they were.

## GET /v1/session/agents/{agentRunId}/compactions/{compactionId}

Reads one immutable child compaction record. `X-Session-ID` is required, and
the record must belong to both the session and the agent run. A compaction ID
from another session or another run returns `404`, so a caller cannot probe for
records it does not own.

```json
{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "fromSequence": 1, "toSequence": 6, "directFromSequence": 3, "directToSequence": 6, "summary": "...", "sourceMessageCount": 6, "parentCompactionId": "uuid", "createdAt": "..."}
```

## POST /v1/session/agents/{agentRunId}/cancel

Cancels one child agent run without ending its parent turn. `X-Session-ID` is
required, no body. Idempotent, same shape and semantics as
`POST /v1/session/cancel`.

```json
{"agentRunId": "uuid", "cancelRequested": true, "state": "running"}
```
