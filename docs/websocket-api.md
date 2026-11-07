---
title: "WebSocket API"
description: "Send messages to Peen and watch turns live over WebSocket, with the peen.v1 subprotocol, frame types, reconnects, and protocol events."
nav_order: 9
---

# WebSocket API

Use WebSocket to send messages to Peen and watch turns as they happen. The [REST API](http-api.md) reads what Peen stored and controls a run.

Messages for the agent go only over a WebSocket upgrade at `GET /ws/v1`. The endpoint is outside the OpenAPI document; its contract is [api/asyncapi.yaml](https://github.com/psyb0t/peen/blob/main/api/asyncapi.yaml) (AsyncAPI 3.0.0), generated into Go types in [`internal/pkg/http/wsapi`](https://github.com/psyb0t/peen/tree/main/internal/pkg/http/wsapi) and TypeScript types in [`web/src/lib/ws/generated.ts`](https://github.com/psyb0t/peen/blob/main/web/src/lib/ws/generated.ts) by `make generate`. An optional `?sessionId={canonical-uuidv4}` asks the server to send that socket only one session's frames. It is a pure filter: it does not create or authorize a session, and closing it, even as the last connection anywhere for that session, neither pauses nor stops anything. Request logging leaves out query strings.

Peen validates every outgoing frame against the AsyncAPI spec before sending it. A frame that fails validation is still sent, because a contract bug must not create a gap in the client's feed; Peen logs the mismatch with the frame's type and sequence instead. The web client does the opposite: a frame that fails validation is treated as unknown, skipped, and not applied, but the client still advances its cursor past it.

A session's work keeps running with no client connected at all. What stops it is `POST /api/v1/session/cancel`, the browser Stop control, `peen session stop`, cancelling a child agent, stopping the controller (`peen stop`) or closing an embedded runtime, a failed turn, or a crash. After a crash Peen restores the session paused, and it stays paused until something explicitly continues it, unless `PEEN_CRASH_RECOVERY` is `resume`. Stop, a failed turn, and a crash each end the turn with a stored `turn.cancelled` or `turn.failed` frame, so a client catching up through the feed sees it end. See [recovery and continuation](agents.md#recovery-after-a-crash).

A client that reconnects, or one catching up for the first time, replays what it missed through the session feed instead of a special resume handshake. Track the last sequence applied per session, and call [`GET /api/v1/session/feed`](http-api.md#get-apiv1sessionfeed) with that value as `cursor` until `hasMore` is false. `GET /api/v1/session` reports the feed's current head as `feedSequence`, and `activeTurnFeedSequence` while a turn is running, so a client picking a session for the first time knows where to start. For example:

```bash
curl "http://127.0.0.1:8080/api/v1/session/feed?cursor=42&limit=100" \
  -H "X-Session-ID: <session-id>"
```

returns the frames after sequence 42, oldest first, with `nextCursor` to continue from and `hasMore` saying whether more are waiting. A client applies each item in order, sets `last` to its `sequence`, and repeats with `cursor=last` while `hasMore` is true. Once caught up it keeps applying live frames as they arrive.

`POST /api/v1/sessions/system` opens or resumes the deployment's one [system session](agents.md#the-system-agent) and starts no turn. It answers `403 SYSTEM_AGENT_OFF` while `PEEN_SYSTEM_AGENT` is off and creates nothing.

The controller starts with no sessions. A client creates one by naming a workspace through `POST /api/v1/sessions/open`, the only operation that creates a workspace session. Sessions are keyed by canonical workspace path, so opening the same directory again, by any of its names, resumes the existing session instead of making a second one. `GET /api/v1/sessions` lists them and takes no session header, because it is how a client finds them.

`GET /api/v1/workspace-roots` returns `{"roots": ["<PEEN_WORKSPACE_ROOT>"]}`, a list that always holds the one workspace root. An authenticated client may offer that root before opening a session. The embedded control surface uses it as a suggestion and still lets a user type an existing directory inside it. The endpoint does not create a session.

A workspace must be the configured `PEEN_WORKSPACE_ROOT` or sit inside it. Peen refuses a workspace outside it with `403 WORKSPACE_NOT_ALLOWED` and creates no session. It refuses a workspace that is, contains, or sits inside `PEEN_STATE_DIR`, or is or contains `PEEN_CONFIG_DIR`, the same way, because a Docker worker mounts its workspace writable. See [where it runs](where-it-runs.md#workspaces-that-would-expose-peens-own-directories). An error from `POST /api/v1/sessions/open` never names the root, so a caller cannot map the deployment's directory layout by probing it. A missing directory inside an allowed root returns `404 WORKSPACE_NOT_FOUND` with `workspace directory does not exist`, so a client can correct the path.

An open request may also name an agent space, `control` or `docker:<profile>`. That is the only environment choice a client makes. Images, mounts, network, and capabilities come from deployment configuration alone. A name the deployment does not offer is refused with `403 AGENT_SPACE_NOT_ALLOWED` and creates no session, even when the workspace already has one. Opening an existing session never changes the agent space it already runs in. `GET /api/v1/agent-spaces` lists what a client may name and the agent space a request without a name gets. Each Docker space reports its `profile`, `docker` (`none`, `readonly`, or `full`), `network` (`none`, `bridge`, or `host`), and `sudo`, plus `hostRootEquivalent` and one sentence per risky grant in `capabilityWarnings`. The `control` space reports no grants. [Where it runs](where-it-runs.md) explains the agent spaces.

`GET /api/v1/session/workers` reports one session's worker generations, newest first. A generation is one run of one worker process. It records the agent space and agent space revision it started under, its lifecycle state, and for a Docker generation its container and repository image digest when one is available. Turns, protocol events after the worker started, jobs, and child-agent runs record the generation that produced them. The record of an accepted user message is written before the worker starts, so it has no generation.

`POST /api/v1/session/reconfigure` moves an open session to a different agent space. The body carries `agentSpace` and `reason`, both required, and nothing else. Schema validation rejects an image, mount, network setting, or capability in the body before a handler sees it. An agent space the deployment does not offer is refused with `403 AGENT_SPACE_NOT_ALLOWED`, and the refusal names no agent space, so a caller cannot list the allowed set by probing. A session with a turn in flight is refused with `409 SESSION_BUSY`, because changing the environment under running work would credit that turn's tool calls to an agent space that did not run them. The same call succeeds once the turn ends. On success Peen stops the session's current worker, so the next turn starts a new generation in the new agent space.

`GET /api/v1/session/agent-space-decisions` reports that history, newest first. Each entry names the agent space the session moved from, the agent space it moved to, the reason the caller gave, and when it was decided.

`GET /api/v1/models` lists every model Peen found at controller startup. Each record gives the exact name a message uses to pick that model, its connection name, the raw model ID the upstream accepted, the context window Peen enforces, and `imageInput`, which reports whether the model takes images. An upstream with `"imageInput": false` in `PEEN_UPSTREAMS` reports `false` for every one of its models. The endpoint never makes a provider call and never returns provider credentials. Clients pass the `name` field as `data.model` in a `message.send` frame.

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
    "systemPrompt": {"mode": "append", "content": "optional instructions"},
    "to": "optional run ID of a running child agent",
    "images": [{"mediaType": "image/png", "data": "optional base64 image bytes"}]
  },
  "metadata": {"sessionId": "canonical UUIDv4 returned by sessions/open"},
  "timestamp": 0,
  "triggeredBy": null
}
```

`systemPrompt.mode` is `append` for the default prompt plus the supplied text, or `replace` for the supplied text alone. Both `mode` and a non-empty `content` are required when `systemPrompt` is present, and leaving either out fails the message with `VALIDATION_FAILED`. Neither setting carries over to the next message.

`reasoningEffort` sets the reasoning level for this turn's model calls. Without it the model uses its own default. Peen fits a level the model cannot take. A model without reasoning levels gets none, and a level outside the model's range runs at the nearest level it supports. The controller logs each change with the requested level, the level used, and a `reason`. Any other value fails the message with `VALIDATION_FAILED`.

`images` attaches up to `PEEN_MAX_IMAGES_PER_MESSAGE` images, 4 by default, that the model sees after the message text. Each has a `mediaType` of `image/png`, `image/jpeg`, `image/gif`, or `image/webp`, and `data` with the base64 bytes, at most `PEEN_MAX_IMAGE_BYTES`, 5 MiB by default, once decoded. Peen checks that the bytes are the format the media type names. Peen stores the images with the message in the session database and replays them to the model on later turns. A message queued into a running turn, or sent `to` a child, may carry images too. Peen stores them with the queued message and shows them to the model when the message reaches the turn. A message fails with `VALIDATION_FAILED` when the model that would see it does not take images, by its driver or by its upstream's `imageInput` setting. For a queued message that is the running turn's model, and for a message to a child it is the child's model. Peen never drops or converts an image to make a message fit. A session whose history holds images can only run turns on a model that takes images. A turn on a text-only model fails with `VALIDATION_FAILED` and stores nothing. The WebSocket accepts frames as large as the message, system prompt, and image limits allow together.

`to` sends the message to a child agent of the session instead of the root conversation. The child reads it after its current tool call, as a `<message from="user">` block, and it is stored in the child's transcript once it reaches the model. Such a message starts no turn and completes at once with `message.completed` `{"queued": true}`. It cannot carry `model`, `reasoningEffort`, or `systemPrompt`, and doing so fails with `VALIDATION_FAILED`. A child whose work a Stop, a crash, or a failure paused resumes, and reads why it was paused before the message. See [messages to a paused child](agents.md#messages-to-a-paused-child). `AGENT_PAUSED` is deprecated and no longer sent. A child that has ended fails it with `AGENT_FINISHED`, a run ID the session never had fails with `AGENT_NOT_FOUND`, and a child whose inbox already holds `PEEN_MAX_QUEUED_USER_MESSAGES` messages fails it with `USER_MESSAGE_QUEUE_FULL`. Use it to answer a child's question or to steer it. See [running children](agents.md#running-children).

`system` is set on a message to the system session and on nothing else. The server fails a mismatch in either direction with `VALIDATION_FAILED`, and `system` cannot combine with `to`.

Session workers accept messages into a child's durable inbox before reconstructing its live conversation. Repeating the same request ID and message reuses the saved acceptance, including its original directory. A tool call that the pause interrupted reaches the resumed child as an error result saying its outcome is unknown, and Peen does not repeat it.

`metadata.sessionId` is required on every client message. It routes the work to an existing session and does not grant access. `data.workspace` is rejected. A client learns a session ID from `POST /api/v1/sessions/open`, not from a special first socket frame.

Write `:skill-name` at the start of a message or after a space to hand that skill to the model. A core skill is `:core:name`. Peen checks every name before it opens a turn or contacts a provider. Each skill reaches the model as its own user message right after your message, as a `<skill>` block with the skill's name, description, directory, and full `SKILL.md`. The directory is absolute for a custom skill and `embedded://skills/<name>` for a core skill. The skill is not part of the system prompt, and a child agent only gets it when the parent names it in the `launchAgent` task. A skill that names other skills with `:name` brings them too, and Peen does not repeat a skill the model already holds, as [Skills](skills.md#once-per-conversation) explains. An unknown name produces `message.failed` with `VALIDATION_FAILED`, and nothing is stored. Without this syntax, the model sees the skill catalogue and decides whether to load a matching skill with `useSkill`. See [Skills](skills.md#forcing-a-skill).

Peen accepts the messages for one session in the order it receives them, from every connected client. The first starts a turn. Every `message.send` that arrives while that turn runs or is still starting, for example while the session's worker boots, joins the turn's user-message queue, first in first out. A queued message may repeat the running turn's `model` and `reasoningEffort`, but it cannot change them or set a `systemPrompt`, because those settings belong to the running turn, and that refusal answers `message.failed` with `VALIDATION_FAILED`. A queued message that names a skill with `:name` is resolved when Peen accepts it. An unknown name refuses that message with `VALIDATION_FAILED` and queues nothing. Otherwise the message and its skill messages are queued together, and they reach the model together. At most `PEEN_MAX_QUEUED_USER_MESSAGES` messages, 16 by default, can wait in the queue at once, counting each skill message or already-loaded note that follows a queued message, and a message that does not fit answers `USER_MESSAGE_QUEUE_FULL`. Queueing never interrupts a provider request in flight.

A queued message gets its own request ID. Peen acknowledges it with `message.completed` `{"queued": true}` and emits `userMessage.created` and then `userMessage.queued`, both carrying that message's own `requestId`. When the running turn's current model round ends, after every tool call in that round has finished, Peen hands the queued message to the model and emits `userMessage.delivered` with that same `requestId` and `{"message": "<text>"}` to every connected client. Queued messages are delivered in the order they were sent, and `userMessage.delivered` fires once per message however many skill messages follow it. Each skill message that follows is announced with a `skill.injected` frame under the running turn's `requestId`. Accepted messages are retained in the durable workflow inbox until delivery, including their request IDs, directory and resolved skills. A missing delivery event does not mean the message was lost. Inspect retained work before resending. Stop leaves pending input stored until explicit continuation; input accepted after a completion boundary can start a separate root. See [continuing retained work](agents.md#continuing-retained-work).

Every accepted `message.send` first emits a stored `userMessage.created` frame. Every server frame is a JSON object with `id`, `type`, `data`, `timestamp`, `metadata`, and `triggeredBy`, the same shape as the client frame above. On `message.completed` and `message.failed`, `triggeredBy` is the `id` of the client frame that caused it. Every sequenced frame carries `null`, because it is rendered from a stored record the same way live and on replay. Nothing causes a client frame, so a client sends `null`. A sequenced frame's `timestamp` is the stored row's creation time, the same whether the frame is sent live or replayed from the feed, so a client cannot use it to tell the two apart. All session frames carry `metadata.sessionId`. Turn frames also carry `metadata.requestId`, and every frame of one turn shares the turn's `requestId`, except the `userMessage.*` frames of a queued message, which carry that message's own. `agent.run.*` and `notice.created` frames carry no `requestId`. A sequenced frame also carries `metadata.sequence`, its position in the session's feed, and its `id` is the id of the stored record rather than a random value; `message.completed` and `message.failed` carry neither, because they are live-only acknowledgements that are never replayed:

```json
{
  "id": "uuid",
  "type": "content_block_delta",
  "data": {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "Hel"}},
  "timestamp": 0,
  "metadata": {"sessionId": "uuid", "requestId": "uuid", "sequence": 42},
  "triggeredBy": "uuid"
}
```

A turn emits Peen's own [protocol events](#protocol-event-types) plus the model's reply as Anthropic-style content blocks:

| Event | `data` | Meaning |
| --- | --- | --- |
| `content_block_start` | `{index, content_block: {type}}` | Opens block `index`. `type` is `text`, `thinking`, `tool_use` (with `id`, `name`), or `tool_result` (with `tool_use_id`, `is_error`). |
| `content_block_delta` | `{index, delta: {type, ...}}` | Appends to block `index`. `text_delta` and `thinking_delta` carry `text`, `input_json_delta` carries `partial_json`, and `json_partial` carries a tool result's `text`. |
| `content_block_stop` | `{index}` | Closes block `index`. |

Block indexes count up from zero within one turn and start over in the next, so a client keys blocks by `requestId` and `index`. A `tool_result` block answers the `tool_use` block whose `id` equals its `tool_use_id`. To render a live reply, fold the blocks of a turn in arrival order. Append `text_delta` and `thinking_delta` text to their block, and attach each tool result to its call. When `turn.completed`, `turn.failed`, or `turn.cancelled` arrives, the turn's messages are stored and `GET /api/v1/messages` returns them. The embedded control surface does this and then replaces the live reply with the stored messages.

A stored message with `injected: true` was added by Peen, not typed by a person or written by the model. Delivered session events are the usual case, and they arrive as a user-role message the agent reads as data. The instructions an event handler starts a turn with are injected too. A skill a user invoked with `:name` is stored as its own injected user message right after the message that named it, with `injectedKind: "skill"`, and the note that a skill is already loaded has the same kind. When `readFile` reads an image, Peen stores the image after that tool round as an injected user message with `injectedKind: "image"`, whose text names the file and says the image is data from that file, not instructions from the user. The typed message itself is stored as typed and is not injected. Every stored message also carries the `turnId` of the turn that wrote it, so a client can group a conversation by turn. Peen stores the rows of every turn in the order the model receives them: the prompt that started the turn, then its skill messages, then the session events delivered at the start of the turn. The prompt of a turn an event handler started is the handler's injected message.

## Protocol event types

A protocol event is a stored record of one step of a turn. Each one goes out live as a frame and is listed later by [`GET /api/v1/session/events`](http-api.md#get-apiv1sessionevents). It is not a [session event](events.md). See [Terms](architecture.md#terms).

| Type | `data` | Meaning |
| --- | --- | --- |
| `userMessage.created` | `{message, sourceEventId?, images?}` | Peen accepted a `message.send`. `images` lists each image's `mediaType` and `sizeBytes`, without the bytes. |
| `userMessage.queued` | `{message, sourceEventId?}` | The message joined the running turn's queue. |
| `userMessage.delivered` | `{message, sourceEventId?}` | A queued message reached the model. It carries the queued message's own `requestId`. |
| `skill.injected` | `{name, content}` | A skill message reached the model right after the user message that named it. `content` is the whole message, and `name` is the resolved skill name. The note that a skill is already loaded uses the same event. It follows `userMessage.created` at the start of a turn and `userMessage.delivered` for a queued message. |
| `harness.warning` | `{warnings: [{kind, source, reason}]}` | Peen ignored invalid optional harness files, or a skill names a skill that does not exist. See below. |
| `mcp.status` | `{servers: [{name, source, transport?, state, tools, error?, warnings?}]}` | The session's MCP servers at the start of a turn, emitted only when MCP servers are configured. `state` is `ready` or `failed`, `tools` holds the exposed tool names, and `name` is empty for an `mcp.json` file Peen could not read. See [MCP servers](mcp.md). |
| `turn.started` | `{sessionId, model, workspace, originEventId?, originEventType?}` | The turn started. `originEventId` and `originEventType` name the session event that woke it, when one did. |
| `session.events` | `{notices, dropped}` | Session events were handed to the model. See [Session events](events.md#how-the-model-receives-events). |
| `tool.use` | `{callId, name, arguments}` | The model asked for a tool call. |
| `tool.result` | `{callId, name, content, isError}` | A tool call finished. |
| `provider.retry` | `{attempt, reason, status, delayMs}` | Peen is retrying a failed provider request. |
| `turn.completed` | `{model, text, finishReason?}` | The turn finished. `text` is the final answer. `finishReason` is why the model stopped that answer, as the provider reported it; `length` means the answer reached the output token limit and was cut off, which can leave `text` empty when the model spent the limit on reasoning. |
| `turn.failed` | `{reason, error?}` | The turn failed. `error` is the error it ended with, without source locations. The same error is also recorded as a `turn.failed` [session event](events.md#turn-events). |
| `turn.cancelled` | `{reason}` | The turn was cancelled. |
| `question.asked` | `{question, callId?}` | The root conversation asked you a question with `ask`. Your next message is the answer. |
| `question.answered` | `{answered, callId?}` | The root's `ask` ended. `answered` is `false` when it timed out. |
| `agent.run.*` | `{agentRunId, parentTurnId, parentAgentRunId?, event}` | A step of a child agent run. The names are `agent.run.started`, `.text.delta`, `.thinking.delta`, `.tool.use`, `.tool.result`, `.assistant.message`, `.message.injected`, `.message.queued`, `.message.delivered`, `.message.undelivered`, `.question`, `.answer`, `.provider.retry`, `.completed`, `.failed`, and `.cancelled`. `event` holds the step's own payload. The queued, delivered and undelivered message steps carry `{message, acceptanceId?}`, where the optional UUID links one accepted message across its outcomes. `.question` carries `{question, callId?}` and `.answer` carries `{answered, callId?}`, where the optional call ID links the same `ask` call. Older stored events omit these IDs. `.assistant.message` carries `{role, content, toolCalls, reasoning?, providerReasoning?, origin?}`: `content` is an array of parts shaped `{type, text?, image?, audio?, file?}`, and each entry of `toolCalls` is `{id, name, arguments}`. `.message.injected` carries `{type, content, phase, tool, callId, round}`, where `type` is `user` or `assistant`. These reach every connected client live, with a `metadata.sequence` in the session feed, the same as root protocol events. They are not in `GET /api/v1/session/events`, which lists only root-turn events; Peen also stores them with the run, and `GET /api/v1/session/agents/{agentRunId}/events` returns them with their own per-run sequence. |
| `notice.created` | the stored `SessionNotice`: `{id, type, source, summary, data?, delivery, createdAt}` | A session notice was recorded, live, whichever `delivery` mode it has and whether or not it has reached the model yet. See [Session events](events.md). |

`content_block_start`, `content_block_delta`, and `content_block_stop` carry the model's reply as shown above. `message.completed` and `message.failed` answer one client frame, as described below, and are not stored.

A successful submission finishes with `message.completed`. Its data is `{"queued": false}` when the turn finished, or `{"queued": true}` when the message joined a running turn's queue. `queued: true` acknowledges only that submission. The running turn hands the queued text to the model when its current model round ends, and `userMessage.delivered` reports that. `message.completed` closes the submission, not the socket. A failed submission finishes with `message.failed`, whose payload is `{code, message, reason?}`. `reason` appears only for known categories that are safe to show. Unknown provider, worker, and tool errors never expose their wrapped details.

An accepted turn may emit `harness.warning` before `turn.started`. Its data is `{"warnings":[{"kind":"skill","source":"...","reason":"..."}]}`. It means Peen ignored one or more invalid optional harness files and loaded the valid configuration around them. The event is stored and visible to every global socket for that session. A bad optional definition does not stop a turn. A `:skill-name` reference to a skill Peen ignored still fails the message.

A malformed client frame gets a private `message.failed` frame on the socket that sent it only. It creates no session and does not reach the global feed. Frames for accepted work, including later turn failures, reach every global socket and every socket filtered to that session. A filtered client does not receive other sessions, so it should use REST when it needs earlier state after switching filters.

When `PEEN_API_TOKEN` is set, clients outside a browser may send the usual `Authorization: Bearer <token>` header with the handshake. Browser clients send two WebSocket subprotocols, `peen.v1` and `peen.bearer.<base64url-token>`, where `base64url-token` is the bearer token in unpadded URL-safe Base64. Peen selects `peen.v1` and authenticates the other value. Never place a bearer token in the URL. Use `wss://` in deployment.
