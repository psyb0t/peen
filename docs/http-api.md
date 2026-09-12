# API: live work and durable state

Use WebSocket to send messages and watch a turn as it happens. Use REST to read
what Peen stored or to control a run. REST never starts a turn. A client can
open several WebSockets for one session and every one sees the same events.

The full REST contract is [api/api.yml](../api/api.yml) (OpenAPI 3.1). This
page explains the parts a client needs to get right.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is
required only when `PEEN_API_TOKEN` is set; see the root
[README](../README.md#things-worth-knowing). Every REST success response
carries `X-Session-ID` and `X-Request-ID` headers. REST errors use one envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## Send and watch turns over WebSocket

Agent messages are submitted only over a WebSocket upgrade at
`GET /v1/ws?sessionId={uuid}`. The endpoint is outside the OpenAPI document
because its contract is WebSocket frames, not HTTP request and response bodies.
`sessionId` is required and is an identifier, not an authentication credential.
Request logging excludes query strings.

A client generates the session UUID. A connection for an existing UUID joins
that session. A connection for a new UUID remains pending until its first
`message.send`; that send atomically creates the session with the supplied UUID.
No REST endpoint creates a session or accepts a user message. Peen maps the
session ID to [Aichteeteapee's WShub](https://github.com/psyb0t/aichteeteapee)
logical client ID, so all sockets for one session receive the same server
events. The server retains WShub's default origin policy: an `Origin` header
must match the request host outside explicitly enabled local development mode.

The accepted client event is `message.send`. Its `data` is strict JSON:

```json
{
  "type": "message.send",
  "data": {
    "message": "required, non-empty",
    "workspace": "optional per-message working-directory override",
    "model": "optional provider/model override for this call only",
    "systemPrompt": {"mode": "append", "content": "optional instructions"}
  }
}
```

`systemPrompt.mode` is `append` for the default prompt plus the supplied text,
or `replace` for the supplied text alone. Neither setting is sticky.

When the session already has a running turn, a `message.send` with only a
`message` joins that turn's FIFO user-message queue. A queued message cannot set
`workspace`, `model`, or `systemPrompt`, because those settings belong to the
running turn. The live queue is bounded by `PEEN_MAX_QUEUED_USER_MESSAGES`,
defaults to 16, and does not interrupt an in-flight provider request. Queued
delivery is process-local until the next provider round. Clients retry after a
restart, cancellation, or an unfinished queue.

Every server frame is a Dabluvee event with `id`, `type`, `data`, `timestamp`,
`metadata`, and `triggeredBy`. Agent events use their native type directly. For
example, a content delta has type `content_block_delta`, not a wrapper type.
All server events carry `metadata.sessionId`, `metadata.requestId`, and the
inbound event ID in `triggeredBy`:

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

The terminal event for one submission is `message.completed`. Its data is
`{"queued": false}` when the turn finished or `{"queued": true}` when the
message joined an active turn's queue. It closes the submission, not the
socket. `message.failed` has a safe `{code, message}` payload. It never exposes
provider errors, file paths, or tool output.

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
resolved content, and manifest. A hash belonging to another session returns
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
{"events": [{"id": "uuid", "sessionId": "uuid", "turnId": "uuid", "sequence": 1, "requestId": "uuid", "type": "tool_call", "payload": {}, "parentToolCallId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
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
`startedAt`, optional `endedAt`, `exitCode` (`-1` while unknown), and
`failureDetail`. The live ring buffers are not this API's source of truth.

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
event count, state and cancellation flag, final text, thinking, response
messages, token counts, failure details, and timestamps. `definition` is
`stored` or `ad-hoc`.

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

## POST /v1/session/agents/{agentRunId}/cancel

Cancels one child agent run without ending its parent turn. `X-Session-ID` is
required, no body. Idempotent, same shape and semantics as
`POST /v1/session/cancel`.

```json
{"agentRunId": "uuid", "cancelRequested": true, "state": "running"}
```
