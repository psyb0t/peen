---
title: "REST API"
description: "Read stored sessions, messages, turns, jobs, and child agents from Peen, and control active work, with every /api/v1 endpoint."
nav_order: 10
---

# REST API

Use REST to read what Peen stored or to control a run. Use the [WebSocket API](websocket-api.md) to send messages and watch turns as they happen. A typed message reaches the agent only over WebSocket. The one REST call that can start a turn is [`POST /api/v1/session/notices`](#post-apiv1sessionnotices), and only when an event handler lets that notice wake an idle session. A normal socket receives live events for every session, and the client decides which session tab to render from each event's metadata.

The full REST contract is [api/api.yml](https://github.com/psyb0t/peen/blob/main/api/api.yml) (OpenAPI 3.1). Peen's embedded browser control surface is served at `/` and is built from the same generated REST types. This page explains the parts another client needs to get right.

Every operation is mounted under `/api/v1`. `Authorization: Bearer <token>` is required only when `PEEN_API_TOKEN` is set, as [network exposure](security.md#network-exposure) explains. Every response carries an `X-Request-ID` header. Every session-scoped success response, meaning any route under `/api/v1/session` and `/api/v1/messages` plus `POST /api/v1/sessions/open`, also carries `X-Session-ID`. Stored reads name their session with the `X-Session-ID` request header. Clients get a session ID from `POST /api/v1/sessions/open` or `GET /api/v1/sessions`. [Terms](architecture.md#terms) explains how the session ID, request ID, turn ID, and `X-Request-ID` differ. REST errors use one envelope:

```json
{"code": "...", "message": "...", "details": {}}
```

## POST /api/v1/sessions/open

Opens the session for a workspace, creating it the first time that directory is opened. No session header. `agentSpace` is optional and sets the agent space only when this call creates the session. Leave it out for the controller's default agent space. A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED`, even when the workspace already has a session.

```json
{"workspace": "/home/me/work/my-app", "agentSpace": "control"}
```

The response is the session, in the same shape as [`GET /api/v1/session`](#get-apiv1session), and whether this call created it:

```json
{"session": {"id": "uuid", "workspace": "/home/me/work/my-app", "agentSpace": "control", "...": "..."}, "created": true}
```

Use `session.id` as `metadata.sessionId` on the WebSocket and as `X-Session-ID` on REST calls.

## GET /api/v1/sessions

Lists every session. No session header. Query parameters are `limit` and `offset`.

```json
{"items": [{"id": "uuid", "workspace": "/home/me/work/my-app", "...": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /api/v1/agent-spaces

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

## GET /api/v1/messages

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
      "images": [{"mediaType": "image/png", "sizeBytes": 48213}],
      "createdAt": "..."
    }
  ],
  "limit": 50,
  "offset": 0,
  "hasMore": false
}
```

`role` is `system`, `user`, `assistant`, or `tool`. A `system` message is one Peen added, such as the message at the end of a turn that did not finish, saying how it ended. The model receives it as a system message. Peen's internal harness records, such as context changes and resolved prompts, are not messages and never appear here.

`images` lists the images a message carries, by media type and size, in the order the model sees them. It is absent when a message has none. The image bytes are not part of the response.

`compactionId` names the stored compaction that summarized this message, when one did. Peen never rewrites it when a later compaction includes the earlier summary.

## GET /api/v1/session/turns

Lists stored turns, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. Each turn includes its request ID, workspace, state, cancellation flag, timestamps, failure category, and the hashes of the context and prompt snapshots used for that turn, when they exist.

## GET /api/v1/session/context-snapshots/{contextHash}

Reads the exact resolved context a turn's `contextSnapshotHash` names. `X-Session-ID` is required. The response includes the hash, creation time, resolved content, and manifest. The manifest is an array of the layers the context was built from, in resolution order, each with `kind`, `name`, `source`, `priority`, and `hash`. A hash belonging to another session returns `404`.

## GET /api/v1/session/prompt-snapshots/{promptHash}

Reads the exact system prompt a turn's `promptSnapshotHash` names. `X-Session-ID` is required. A hash belonging to another session returns `404`.

## GET /api/v1/session/learned

Lists the learned rules and skills the session loads: those under `.agents/learned/` in the session's workspace first, then those in the global learned directory, each group sorted by kind and name. `X-Session-ID` is required. Each item has `scope` (`workspace` or `global`), `kind` (`rule` or `skill`), `name`, the host `path`, `sizeBytes`, `modifiedAt`, `estimatedTokens`, and `alwaysLoaded`. `alwaysLoaded` is `true` for a rule without `paths`, `globs`, or `applyTo`, which is part of every system prompt and counts against [the prompt budget](harness.md#the-prompt-budget). A rule's `name` is its path under `rules/` without `.md`, such as `go/errors`. A skill's `name` is its directory name. Symbolic links are left out. See [auto-evolve](harness.md#auto-evolve).

## GET /api/v1/session/mcp

Reports the session's MCP servers as the worker last saw them. `X-Session-ID` is required. The response is `{servers, reportedAt}`. `servers` is the data of the latest `mcp.status` event, and `reportedAt` is when the worker emitted it, at the start of a turn. Before any turn has reported, `servers` is empty and `reportedAt` is `null`. A server that stopped after that turn started still shows as `ready` until the next turn reports. See [MCP servers](mcp.md#status).

## GET /api/v1/session/learned/file

Reads one learned rule, or a learned skill's `SKILL.md`, named by the `scope`, `kind`, and `name` query parameters. `X-Session-ID` is required. The response is `{file, content}`, and `file` has the fields of a list item. A name with an empty, `.`, or `..` segment, or a skill name with `/`, returns `400`. A file that does not exist, or a path that leaves the learned directory through a symbolic link, returns `404`.

## DELETE /api/v1/session/learned/file

Deletes the named learned rule's file, or the learned skill's whole directory, and answers `204`. It takes the same parameters and returns the same `400` and `404` errors as the read. The next turn, child launch, or `changeDirectory` call no longer loads the file. Nothing outside a learned directory can be deleted this way.

## GET /api/v1/session/compactions

Lists the session's compaction records, newest first. A compaction record never changes once written. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

Each record has the exact source message range, summary, model, prompt hash, token counts, and an optional `parentCompactionId`. The parent link forms a tree. When a later summary includes an earlier summary plus new raw messages, the later record points at the earlier one. Existing messages keep their original `compactionId`. Follow parent IDs to rebuild the whole lineage.

## GET /api/v1/session/compactions/{compactionId}

Reads one compaction record. `X-Session-ID` is required. The record includes the source range it covered itself and an optional `parentCompactionId`, so a client can read any point in the lineage without inferring it from message order.

## GET /api/v1/session/model-runs

Lists each provider call Peen recorded for the session, where one call may take several provider rounds. `X-Session-ID` is required. Query parameters are `limit`, `offset`, an optional `stage` (`turn`, `child`, or `compaction`), and an optional `state`.

A model run stores the requested and returned model identity, connection name, the settings in effect apart from secrets, text and thinking output, response messages and injections, provider usage, every cost amount, timing, and failure details. `agentRunId` names the child-agent run when a child made the call.

## GET /api/v1/session/model-runs/{modelRunId}/calls

Lists the exact provider rounds of one model run. `X-Session-ID` is required. Query parameters are `limit` and `offset`. The response includes the model run and each round's request messages, the tool definitions the provider saw, response message, usage, retry attempts, token categories, cost amounts, model identity, timing, and failure details. A model run ID from another session returns `404`.

## GET /api/v1/session

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
  "feedSequence": 43,
  "activeTurnFeedSequence": null,
  "agent": "core:general-purpose",
  "model": "aigate/your-model-id",
  "workspace": "/home/me/work/my-app",
  "agentSpace": "control"
}
```

`feedSequence` is the session's newest feed sequence, the `head` value [`GET /api/v1/session/feed`](#get-apiv1sessionfeed) would return right now. `activeTurnFeedSequence` is the first feed sequence of the running turn, or `null` when no turn is running; replay from the sequence before it to rebuild that turn's live view.

## POST /api/v1/session/cancel

Pauses unfinished session workflows and interrupts their current activities, including children of turns that already ended. `X-Session-ID` is required, and there is no body. It returns `202` whether or not anything was running, so repeating it is safe.

```json
{"cancelRequested": true}
```

`cancelRequested` is `true` when this call cancels a pending admission, signals a running turn or child, or pauses an unfinished workflow. Stop records the pause before interrupting the worker. The endpoint does not wait for every activity to wind down. A request accepted before Stop cannot start afterward, and reconnecting does not resume paused work.

An active foreground `runCommand` kills its process group when interrupted. Commands whose running job handles already returned keep their detached behavior. Their completion notices cannot wake the stopped source work. See [process jobs](configuration.md#process-jobs).

## POST /api/v1/session/reconfigure

Moves an idle session to another agent space. `X-Session-ID` is required. Both fields are required, and any other field is rejected.

```json
{"agentSpace": "docker:builder", "reason": "needs to build container images"}
```

The response is the recorded decision:

```json
{"id": "uuid", "sessionId": "uuid", "fromAgentSpace": "docker:default", "toAgentSpace": "docker:builder", "reason": "needs to build container images", "decidedAt": "..."}
```

A name the deployment does not offer returns `403 AGENT_SPACE_NOT_ALLOWED`. A session with a turn in flight returns `409 SESSION_BUSY`. On success Peen stops the session's worker, and the next turn starts a new worker generation in the new agent space.

## GET /api/v1/session/agent-space-decisions

Lists one session's agent space changes, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

```json
{"items": [{"id": "uuid", "sessionId": "uuid", "fromAgentSpace": "docker:default", "toAgentSpace": "docker:builder", "reason": "needs to build container images", "decidedAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /api/v1/session/workers

Lists one session's worker generations, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. `containerId` and `imageDigest` appear only for a Docker generation, `imageDigest` only when the daemon reported a repository digest, and `failureDetail` only for a generation that failed or was replaced.

```json
{"items": [{"id": "uuid", "sessionId": "uuid", "kind": "docker", "agentSpace": "docker:builder", "agentSpaceRevision": 2, "state": "ready", "workspace": "/home/me/work/my-app", "containerId": "...", "imageDigest": "psyb0t/peen@sha256:...", "createdAt": "...", "startedAt": "...", "endedAt": null}], "limit": 50, "offset": 0, "hasMore": false}
```

`state` is `requested`, `starting`, `ready`, `stopping`, `stopped`, or `failed`.

## GET /api/v1/session/events

Lists the protocol events Peen stored while it handled the session. `X-Session-ID` is required. Query parameters are `limit`, `offset`, and `order` (`asc` or `desc`).

```json
{"events": [{"id": "uuid", "sessionId": "uuid", "turnId": "uuid", "workerGenerationId": "uuid", "sequence": 1, "requestId": "uuid", "type": "tool.use", "payload": {}, "parentToolCallId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

These are records of what happened, not a queue. Listing never consumes or changes them. The [protocol event types](websocket-api.md#protocol-event-types) table lists every `type`.

## GET /api/v1/session/feed

Replays a session's sequenced live frames after `cursor`, oldest first: root protocol events, child `agent.run.*` steps, and notices. `X-Session-ID` is required. Query parameters are `cursor` (int64, default 0, return items with a sequence greater than this value) and `limit` (1 to 200, default 100).

```json
{
  "items": [
    {"sequence": 43, "id": "uuid", "type": "tool.use", "sessionId": "uuid", "requestId": "uuid", "data": {}, "createdAt": "..."}
  ],
  "limit": 100,
  "nextCursor": 43,
  "hasMore": false,
  "head": 43
}
```

Each item has the same `type`, `data`, and `sequence` its live WebSocket frame carried, and `id` is the id of the stored record, so a client that reconnects passes the last sequence it applied as `cursor` and continues without a gap or a duplicate. `head` is the session's newest feed sequence when the page was read. A cursor at or past the head returns an empty page with `nextCursor` equal to the `cursor` sent. `message.completed` and `message.failed` are never in this list, because they are unsequenced live acknowledgements. See [disconnect and reconnect](websocket-api.md) above.

## GET /api/v1/session/notices

Lists stored notices from every source in [Session events](events.md#where-events-come-from). `X-Session-ID` is required. Query parameters are `limit` and `offset`.

## POST /api/v1/session/notices

Records a notice from outside Peen, such as a webhook, a CI run, or an operator. `X-Session-ID` is required.

```json
{"type": "app.error", "summary": "one line", "data": {}, "delivery": "queue"}
```

`type` is a lowercase dotted name up to 128 characters. The `job.` and `agent.` prefixes are reserved for Peen's own events and are rejected here. `summary` is required and must not be empty. A `summary` longer than `PEEN_MAX_EVENT_SUMMARY_BYTES` or `data` larger than `PEEN_MAX_EVENT_DATA_BYTES` is rejected with `400 VALIDATION_FAILED`.

`delivery` is `queue`, the default, which delivers the notice at the next turn or tool boundary, or `wake`. A notice starts a turn only when a handler for its type exists in some harness layer at `<layer>/.agents/events/<type>.md`. A handler that sets `delivery` decides on its own, and a handler that leaves it out uses the notice's `delivery`. The other wake conditions, such as an idle session and the hourly wake limit, are in [waking an idle session](events.md#waking-an-idle-session). A notice that does not wake is queued. Notice content is untrusted, so Peen quotes it to the model as data and never merges it into the system prompt.

## GET /api/v1/session/jobs

Lists the session's process jobs, newest first. `X-Session-ID` is required. Query parameters are `limit`, `offset`, and an optional `state` filter (`running`, `exited`, `signalled`, `failed`, `interrupted`).

Each entry is the whole stored job record: `jobId`, `sessionId`, `turnId`, `pid`, `purpose`, `command`, `directory`, optional `toolCallId`, `state`, optional `workerGenerationId`, `startedAt`, optional `endedAt`, `exitCode` (`-1` while unknown), and `failureDetail`.

## GET /api/v1/session/jobs/{jobId}/output

Reads a window of one job's output without waiting for the job to finish. `X-Session-ID` is required. Query parameters are `stream` (`stdout`, `stderr`, or `both`, default `both`), `cursor` (default 0), and `limit` (1 to 200, default 50). The response holds ordered `lines`, each with its record ID, `sessionId`, `jobId`, a `sequence` shared by both streams, stream, content, and creation time. Use `nextCursor` to continue. With `stream=both`, the sequence keeps the order Peen saw stdout and stderr lines in. Output comes from SQLite, so a client that reconnects sees the same lines after the job has ended.

## GET /api/v1/session/jobs/{jobId}/signals

Lists every request to stop one job, oldest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`. The response includes the whole job record plus the signal records: their IDs, session and job IDs, the requested signal, whether it reached a live process, the job's state at the time, and the timestamp.

## POST /api/v1/session/jobs/{jobId}/signal

Stops one job. `X-Session-ID` is required.

```json
{"signal": "stop"}
```

`stop` sends `SIGTERM` to the job's process group, then `SIGKILL` if it keeps running. `kill` sends `SIGKILL` at once. A request against an existing job that no longer has a live process is recorded and changes nothing, with `signalled: false`. An unknown job returns `404`.

## GET /api/v1/session/agents

Lists the session's child agent runs, newest first. `X-Session-ID` is required. Query parameters are `limit`, `offset`, an optional `state` filter, and an optional `parentAgentRunId` filter.

`state` is `running`, `waiting`, `completed`, `failed`, `cancelled`, or `interrupted`. `waiting` keeps the running runs that asked a question nobody has answered yet. `parentAgentRunId` keeps the direct children of one run, or the children of the root conversation when it is `root`. Any other value fails with `400 VALIDATION_FAILED`.

Each entry is the whole stored run record: root and parent IDs, task, the instructions in effect, allowed tools, system prompt, workspace and model identity, worker generation, event count, state and cancellation flag, final text, thinking, response messages, token counts, failure details, and timestamps. `definition` is `stored` or `ad-hoc`. `waiting` is `true` while the run waits on a question, and `question` holds that question. A run that is not waiting has no `question` field. `paused` is `true` while a running run's work is paused by a Stop, a crash, or a failure, and is absent otherwise. A message to the run resumes it. Peen reads both from the run's `agent.run.question` and `agent.run.answer` events, so a run is waiting from its question until its `ask` ends.

## GET /api/v1/session/agents/{agentRunId}

Reads one stored child agent run. `X-Session-ID` is required. The record includes its parent tool call, depth, model response, token counts, failure details, `waiting` and `question`, and start and end times.

## GET /api/v1/session/agents/{agentRunId}/events

Follows one child agent run's events. `X-Session-ID` is required. Query parameters are `cursor` (default 0) and `limit` (1 to 200, default 100).

```json
{"agentRunId": "uuid", "state": "running", "events": [{"sequence": 1, "type": "...", "payload": {}, "createdAt": "..."}], "nextCursor": 1, "hasMore": false}
```

A run ID belonging to another session returns `404`, the same as an unknown one, so a caller cannot use this to probe for another session's run IDs.

## GET /api/v1/session/agents/{agentRunId}/messages

Lists one child agent run's own transcript, oldest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

A child agent runs in a separate model context, so its conversation lives apart from the session transcript and never appears under `GET /api/v1/messages`. The first record is the task its parent gave it, as a `<message>` block naming the parent. The rest are the assistant messages, tool results, hook injections, and messages from other agents or from you that the child saw. Each record carries `agentRunId`, `sequence`, `role`, `content`, the `isError` and `incomplete` flags, optional `model`, `thinking`, `toolCalls`, `toolCallId`, and `images`, and `compactionId` when a child compaction covers it. An image the child's `readFile` read is stored as a user message after that tool round.

```json
{"messages": [{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "sequence": 1, "role": "user", "content": "...", "isError": false, "incomplete": false, "compactionId": null, "createdAt": "..."}], "limit": 50, "offset": 0, "hasMore": false}
```

## GET /api/v1/session/agents/{agentRunId}/compactions

Lists one child agent run's compaction records, newest first. `X-Session-ID` is required. Query parameters are `limit` and `offset`.

These records belong to the child. A child compaction never covers a session message and never replaces a session compaction, so this route and `GET /api/v1/session/compactions` describe separate lineages.

Each record has the source message range, the range it covered itself, summary, model, prompt hash, token counts, and an optional `parentCompactionId`. A later child summary covers the earlier summary plus new raw messages and points at the earlier record. The earlier record's own range and the `compactionId` on its messages stay as they were.

## GET /api/v1/session/agents/{agentRunId}/compactions/{compactionId}

Reads one child compaction record. `X-Session-ID` is required, and the record must belong to both the session and the agent run. A compaction ID from another session or another run returns `404`, so a caller cannot probe for records it does not own.

```json
{"id": "uuid", "sessionId": "uuid", "agentRunId": "uuid", "fromSequence": 1, "toSequence": 6, "directFromSequence": 3, "directToSequence": 6, "summary": "...", "sourceMessageCount": 6, "parentCompactionId": "uuid", "createdAt": "..."}
```

## POST /api/v1/session/agents/{agentRunId}/cancel

Cancels one child agent run without ending its parent turn. The unfinished children it launched stop with it. `X-Session-ID` is required, and there is no body. Session-worker workflows record a terminal `cancelled` outcome and retain their context and history. Peen then interrupts their activities and cancels their SDK workflows. Repeating the request also retries delivery to the worker. A replacement worker applies retained cancellations before starting work. Session Stop pauses unfinished workflows instead. See [stopping a child](agents.md#stopping-a-child).

```json
{"agentRunId": "uuid", "cancelRequested": true, "state": "cancelled"}
```
