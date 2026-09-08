# HTTP API reference

Full contract in [api/api.yml](../../api/api.yml) (OpenAPI 3.1). This page is
a readable summary; the spec is the source of truth and every request is
validated against it before it reaches a handler.

Every operation is mounted under `/v1`. `Authorization: Bearer <token>` is
required only when `PEEN_API_TOKEN` is set; see the root
[README](../../README.md#bearer-authentication). Every success response
carries `X-Session-ID` and `X-Request-ID` headers. Errors use one envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## POST /v1/messages

Runs one agent turn. `X-Session-ID` is optional: omit it to start a new
session, or send an existing one to resume it.

Request body:

```json
{
  "message": "required, non-empty",
  "workspace": "optional per-message working-directory override",
  "model": "optional provider/model override for this call only",
  "systemPrompt": {"mode": "append", "content": "optional instructions"}
}
```

`systemPrompt.mode` is `append` (default prompt plus this text, for this
turn only) or `replace` (this text instead of the default prompt, for this
turn only). Neither field is sticky.

When `X-Session-ID` names a turn currently running in this Peen process and
the request contains only `message`, Peen accepts it into that turn's FIFO
user-message queue instead of opening another turn. It returns JSON `202`:

```json
{"queued": true}
```

This applies even when the request's `Accept` header asks for SSE. The active
request remains the only live stream. A queued request cannot set `workspace`,
`model`, or `systemPrompt`, because those settings belong to the already
running turn. Peen writes a durable acceptance audit record before queueing,
then writes the ordinary user transcript row at the provider round where
Elelem actually delivers it. This preserves tool-result ordering.

The live queue is bounded by `PEEN_MAX_QUEUED_USER_MESSAGES`, defaults to 16,
and does not interrupt an in-flight provider request. A full queue returns
`409` with code `USER_MESSAGE_QUEUE_FULL`. Queued delivery state is
process-local until delivery. After a restart, cancellation, or a round limit
that ends the turn before delivery, the acceptance audit does not cause Peen
to replay the message automatically. Clients that need delivery across those
boundaries must retry and tolerate duplicates.

Response framing is chosen by `Accept`:

- Missing or `application/json`: waits for the turn and returns
  `{"message": "..."}`, the agent's final text.
- `text/event-stream`: a live Server-Sent Events stream using Chatz's event
  names and framing (`message_start`, `content_block_start`,
  `content_block_delta`, `content_block_stop`, `message_delta`,
  `message_stop`, `ping`, `chat_status`, `error`).

The seven content-block events describe the assistant message and are produced
by `essessey/elelemstream`, so their shapes are that library's. Two more events
describe the stream itself rather than the message, and their shapes are copied
from Chatz so a Chatz stream parser needs no translation.

`chat_status` is advisory progress. Treat it as ephemeral: it is never stored
and never part of the assistant's content.

```text
event: chat_status
data: {"type":"chat_status","status":"streaming"}
```

`status` is one of `connecting`, `waiting_first_token`, `streaming`,
`running_tool`, or `retrying`. A repeated status is not resent.

`error` is terminal. It appears only after the response headers are committed,
which is the point where the JSON error envelope is no longer reachable.
Cancelling a turn is a normal end to a stream and emits no `error` event.

```text
event: error
data: {"type":"error","error":{"type":"request_failed","message":"The model request failed. Try again."}}
```

`error.type` is one of `upstream_timeout`, `rate_limited`,
`model_unavailable`, `context_limit`, or `request_failed`, each with a fixed
user-facing `message`. The classification is deliberately the only detail that
crosses the wire: a raw failure carries file paths, provider response bodies,
and whatever a tool printed, and that stays in the transcript and the
operator's logs.

`409` means the session already has a turn running, this turn was cancelled
after it started running, or the active user-message queue is full.

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
