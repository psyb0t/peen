# HTTP API reference

Full contract in [api/api.yml](../../api/api.yml) (OpenAPI 3.1). This page is
a readable summary; the spec is the source of truth and every request is
validated against it before it reaches a handler.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is
required only when `PEEN_API_TOKEN` is set; see the root
[README](../../README.md#bearer-authentication). Every REST success response
carries `X-Session-ID` and `X-Request-ID` headers. REST errors use one envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## WebSocket turns

Agent messages are submitted only over a WebSocket upgrade at
`GET /v1/ws?sessionId={uuid}`. The endpoint is outside the OpenAPI document
because its contract is WebSocket frames, not HTTP request and response bodies.
`sessionId` is required and is an identifier, not an authentication credential.
Request logging excludes query strings.

A client generates the session UUID. A connection for an existing UUID joins
that session. A connection for a new UUID remains pending until its first
`message.send`; that send atomically creates the session with the supplied UUID.
No REST endpoint creates a session or accepts a user message. Peen maps the
session ID to WShub's logical client ID, so all sockets for one session receive
the same server events. The server retains WShub's default origin policy: an
`Origin` header must match the request host outside explicitly enabled local
development mode.

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

Lists the session's pending events without consuming them. `X-Session-ID` is
required.

```json
{"events": [{"id": "uuid", "type": "app.error", "source": "...", "summary": "...", "data": {}, "delivery": "queue", "createdAt": "..."}], "dropped": 0}
```

## POST /v1/session/events

Reports an event to the session from outside Peen (a webhook, a CI run, an
operator). `X-Session-ID` is required.

```json
{"type": "app.error", "summary": "one line", "data": {}, "delivery": "queue"}
```

`type` is a lowercase dotted name up to 128 characters. The `job.` and
`agent.` prefixes are reserved for Peen's own producers and are rejected here.
`delivery` is `queue` (default: delivered at the next turn or tool boundary)
or `wake` (starts a turn immediately if the session is idle and
`.agents/events/<type>.md` declares a handler; otherwise degrades to `queue`).
Event content is untrusted: it reaches the model quoted as data, never merged
into the system prompt.

## GET /v1/session/jobs

Lists the session's process jobs, newest first. `X-Session-ID` is required.
Query parameters: `limit`, `offset`, and an optional `state` filter
(`running`, `exited`, `signalled`, `failed`).

Each entry: `jobId`, `pid`, `purpose`, `command`, `directory`, `toolCallId`,
`state`, `startedAt`, `endedAt`, `exitCode` (`-1` while unknown),
`stdoutBufferedLines`, `stdoutDroppedLines`, `stderrBufferedLines`,
`stderrDroppedLines`.

## GET /v1/session/jobs/{jobId}/output

Reads a bounded window of one job's output without waiting for it to finish.
`X-Session-ID` is required. Query parameters: `stream` (`stdout`, `stderr`,
or `both`, default `both`), `stdoutCursor`, `stderrCursor` (both default 0),
`maxLines` (1-2000, default 500). The response reports `nextStdoutCursor` and
`nextStderrCursor` to continue from, plus how many lines were dropped before
this window because the job's ring buffer filled.

## POST /v1/session/jobs/{jobId}/signal

Stops one job. `X-Session-ID` is required.

```json
{"signal": "stop"}
```

`stop` sends `SIGTERM` to the job's process group, then escalates to
`SIGKILL`; `kill` goes straight to `SIGKILL`. Idempotent: signalling an
unknown, exited, or already-signalled job reports `signalled: false` with the
current state rather than failing.

## GET /v1/session/agents

Lists the session's child agent runs, newest first. `X-Session-ID` is
required. Query parameters: `limit`, `offset`, and an optional `state` filter
(`running`, `completed`, `failed`, `cancelled`).

Each entry: `agentRunId`, `name`, `definition` (`stored` or `adhoc`),
`parentToolCallId`, `depth`, `state`, `startedAt`, `endedAt`,
`eventBufferedCount`, `eventDroppedCount`.

## GET /v1/session/agents/{agentRunId}/messages

Follows one child agent run's events. `X-Session-ID` is required. Query
parameters: `cursor` (default 0) and `limit` (1-500, default 100).

```json
{"agentRunId": "uuid", "state": "running", "events": [{"sequence": 1, "type": "...", "payload": {}, "createdAt": "..."}], "nextCursor": 1, "dropped": 0}
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
