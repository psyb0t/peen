# API: live work and durable state

Use WebSocket to send messages and watch turns as they happen. Use REST to read
what Peen stored or to control a run. REST never starts a turn. A normal socket
receives live events for every session. The client decides which session tab to
render from each event's metadata.

The full REST contract is [api/api.yml](../api/api.yml) (OpenAPI 3.1). Peen's embedded browser control surface is served at `/` and is built from the same generated REST types. This page explains the parts another client needs to get right.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is
required only when `PEEN_API_TOKEN` is set; see the root
[README](../README.md#things-worth-knowing). Every REST success response
carries `X-Session-ID` and `X-Request-ID` headers. Durable reads are addressed
to a known session through `X-Session-ID`; clients learn the runtime's
workspace session UUID from the global WebSocket feed. REST errors use one
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

A workspace must sit under a configured `PEEN_WORKSPACE_ROOTS` entry. Peen
refuses one outside every root with `403 WORKSPACE_NOT_ALLOWED` and creates no
session. The refusal names no root, so a caller cannot map the deployment's
directory layout by probing it.

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
user message. Peen gives each socket a server-controlled
[Aichteeteapee WShub](https://github.com/psyb0t/aichteeteapee) identity, then
fans each accepted session event to all global sockets and to sockets filtered
for that session. The server retains WShub's default origin policy: an `Origin`
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
    "systemPrompt": {"mode": "append", "content": "optional instructions"}
  },
  "metadata": {"sessionId": "canonical UUIDv4 returned by sessions/open"},
  "timestamp": 0,
  "triggeredBy": null
}
```

`systemPrompt.mode` is `append` for the default prompt plus the supplied text,
or `replace` for the supplied text alone. Neither setting is sticky.
`metadata.sessionId` is required on every client message. It routes the work to an existing session and is not an authorization grant. `data.workspace` is rejected. A client learns a session ID from `POST /v1/sessions/open`, not from a special first socket frame.

Write a standalone `:skill-name` at the start of a message or after whitespace
to require that exact resolved skill for the turn. Peen validates the name
before it opens a turn or contacts a provider. The full `SKILL.md` reaches the
root and any child agents. An unknown name produces `message.failed`; the user
message remains unchanged. Without this syntax, the model sees the skill
catalogue and decides whether to load a matching procedure with `use_skill`.

When the session already has a running turn, a `message.send` with only a
`message` joins that turn's FIFO user-message queue. A queued message cannot set
`model` or `systemPrompt`, because those settings belong to the running turn.
It also cannot directly activate a skill, because the running prompt is fixed.
The live queue is bounded by `PEEN_MAX_QUEUED_USER_MESSAGES`,
defaults to 16, and does not interrupt an in-flight provider request. Queued
delivery is process-local until the next provider round. Clients retry after a
restart, cancellation, or an unfinished queue.

Every accepted `message.send` first emits a durable `user_message.created`
event. Every server frame is a Dabluvee event with `id`, `type`, `data`,
`timestamp`, `metadata`, and `triggeredBy`. Agent events use their native type
directly. For example, a content delta has type `content_block_delta`, not a
wrapper type. All session events carry `metadata.sessionId`,
`metadata.requestId`, and the inbound event ID in `triggeredBy`:

```json
{
  "id": "uuid",
  "type": "content_block_delta",
  "data": {},
  "timestamp": 0,
  "metadata": {"sessionId": "uuid", "requestId": "uuid"},
  "triggeredBy": "uuid"
}
```

Successful submissions finish with `message.completed`. Its data is
`{"queued": false}` when the turn finished or `{"queued": true}` when the
message joined an active turn's queue. `queued: true` acknowledges only that
submission; the existing turn processes the queued text at its next provider
round boundary. `message.completed` closes the submission, not the socket.
An unsuccessful submission finishes with `message.failed`, whose safe
`{code, message}` payload never exposes provider errors, file paths, or tool
output.

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
  "model": "aigate/your-model-id"
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
{"events": [{"id": "uuid", "sessionId": "uuid", "turnId": "uuid", "workerGenerationId": "uuid", "sequence": 1, "requestId": "uuid", "type": "tool_call", "payload": {}, "parentToolCallId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

These are transcript protocol records, not a queue. Listing never consumes or
alters them.

## GET /v1/session/notices

Lists durable notices received from outside Peen or emitted by its workers.
`X-Session-ID` is required. Query parameters are `limit` and `offset`.

## POST /v1/session/notices

Records a notice from outside Peen, such as a webhook, CI run, or operator.
`X-Session-ID` is required.

```json
{"type": "app.error", "summary": "one line", "data": {}, "delivery": "queue"}
```

`type` is a lowercase dotted name up to 128 characters. The `job.` and
`agent.` prefixes are reserved for Peen's own producers and are rejected here.
`delivery` is `queue` (default, delivered at the next turn or tool boundary)
or `wake` (starts a turn immediately if the session is idle and
`.agents/events/<type>.md` declares a handler; otherwise becomes `queue`).
Notice content is untrusted. Peen quotes it as data for the model and never
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
