---
title: "OpenAI API"
description: "Use OpenAI Chat Completions clients to run Peen turns, continue sessions, stream replies, and request validated JSON answers."
nav_order: 11
---

# OpenAI API

Set `PEEN_OPENAI_API=true` to enable the optional OpenAI Chat Completions surface at `/openai/v1`. It is off by default, and while it is off every path under `/openai/v1` answers a plain `404`. It uses the same bearer `PEEN_API_TOKEN` as the [REST API](http-api.md). An empty token disables authentication. This endpoint runs a coding agent with its configured tools and access, so read [Security](security.md) before exposing it.

## Quick start

Start a conversation in a workspace. `-i` prints the response headers, which carry the session ID:

```bash
curl -i http://127.0.0.1:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $PEEN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Peen-Workspace: /home/me/work/my-app" \
  -d '{"messages": [{"role": "user", "content": "List the failing tests."}]}'
```

Continue the same session by sending the `X-Peen-Session-ID` value from that response:

```bash
curl http://127.0.0.1:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $PEEN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Peen-Session-ID: <session-id>" \
  -d '{"messages": [{"role": "user", "content": "Fix the first one."}]}'
```

An OpenAI SDK takes `http://127.0.0.1:8080/openai/v1` as its base URL and the API token as its key. This uses the official Python package:

```python
import os

from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8080/openai/v1",
    api_key=os.environ.get("PEEN_API_TOKEN") or "unused",
    default_headers={"X-Peen-Workspace": "/home/me/work/my-app"},
    max_retries=0,
    timeout=3600,
)

model = client.models.list().data[0].id
raw = client.chat.completions.with_raw_response.create(
    model=model,
    messages=[{"role": "user", "content": "List the failing tests."}],
)
session_id = raw.headers.get("X-Peen-Session-ID")
print(raw.parse().choices[0].message.content)
```

Set `max_retries=0`: SDKs retry a `409` or a `5xx` by default, and a retried request runs another turn. Raise the client timeout to cover the whole turn. Pass `extra_headers={"X-Peen-Session-ID": session_id}` on a later call to continue the session.

## Routes and sessions

`GET /openai/v1/models` lists the configured model IDs as OpenAI model objects, with `created` set to `0` and `owned_by` set to the model's connection name. `POST /openai/v1/chat/completions` runs one agent turn. Use one of those model IDs, or omit `model` for the deployment default.

Without `X-Peen-Session-ID`, Peen creates fresh conversation history in the workspace named by `X-Peen-Workspace`, falling back to `PEEN_OPENAI_WORKSPACE`. A missing workspace answers 400. The directory must already exist and pass the deployment's workspace policy. Fresh conversations share that directory's files, but have separate histories. They run in the workspace's agent space: the one its existing sessions use, or the default agent space when the workspace has no session yet. Ordinary workspace opens still resume their canonical session.

A successful response carries `X-Peen-Session-ID`. Send it on a later request to continue that session. A non-streaming error response carries no session header, so a fresh conversation whose first turn fails cannot be continued by ID; find it with [`GET /api/v1/sessions`](http-api.md#get-apiv1sessions) if you need it. A session ID must be a canonical UUID. When you continue a session, only the last message is read and sent; earlier messages and system prompts in the request are ignored. The last message must have role `user`. A running turn refuses another request as busy rather than queueing it. System sessions cannot be reached through this surface.

For a fresh conversation, system messages become an appended system prompt, joined in order. Earlier user and assistant messages become escaped text in a `<priorConversation>` block before the last user message. Images are attached in their original order, and each leaves a numbered `[Attached image N]` placeholder where it appeared, in the history or in the last message. These messages and images enter the durable transcript and provider requests.

## Request fields

| Field | Behavior |
| --- | --- |
| `messages` | Roles `system`, `developer`, `user` and `assistant`. `developer` is treated as `system`. Content is a string, or an array of `text` and `image_url` parts; several `text` parts are joined with no separator. |
| `model` | A configured model ID, or empty for the default. |
| `reasoning_effort` | `minimal`, `low`, `medium`, `high`, `xhigh` or `max`. |
| `response_format.type: text` | The same as leaving `response_format` out. |
| `response_format.type: json_object` | Requires a JSON object as the final answer. |
| `response_format.type: json_schema` | Uses `json_schema.schema` as the final-answer JSON Schema. External references are refused. |
| `stream` | Sends Server-Sent Events when true. |

Requests are refused with `400` and a message naming the field in these cases:

- `tools`, `tool_choice`, `functions` or `logprobs` is present with any non-null value, including `false` or an empty array. Peen runs its own tools; this surface does not offer client-executed tool calls.
- `n` is present and is not `1`.
- `model` names a model Peen does not have, or `reasoning_effort` is not one of the values above.
- A message has any other role, such as `tool`, or a content part has another type.
- `response_format.type` is not one of the three above, or `json_schema` has no `json_schema` object, or its schema is invalid, over `PEEN_MAX_OUTPUT_SCHEMA_BYTES`, or needs external resources.
- The last message is not a `user` message, or its text is empty.
- `X-Peen-Session-ID` or `X-Peen-Workspace` is sent more than once.
- The body has trailing data after the JSON object.

Every other field, such as `temperature`, `max_tokens`, `stop`, `stream_options`, `json_schema.name`, `json_schema.strict`, or `image_url.detail`, is ignored.

## Limits

The request body must be `application/json` (otherwise `415`) and fit the same frame limit as a WebSocket `message.send` (otherwise `413`). On a fresh conversation the prior messages are folded into the new user message, so the whole history plus the last message must fit `PEEN_MAX_MESSAGE_BYTES`, and the joined system messages must fit `PEEN_MAX_SYSTEM_PROMPT_BYTES`. [Configuration](configuration.md) lists the defaults.

Images must be base64 `data:` URLs of PNG, JPEG, GIF or WebP whose bytes match the named type, each at most `PEEN_MAX_IMAGE_BYTES`. `PEEN_MAX_IMAGES_PER_MESSAGE` counts every image in the request, not each message. Remote HTTP and HTTPS images are refused without fetching them. Image parts on system, developer or assistant messages are refused.

## Replies

A non-streaming reply is a `chat.completion` object. Its assistant content is the final text, or the validated JSON text when a response format was requested. `usage` totals the turn's audited model calls, including failed provider attempts, and is zero when no usage was recorded. `finish_reason` is `length` when the turn hit `PEEN_MAX_TOOL_ROUNDS` or `PEEN_MAX_TURN_TOKENS`, and `stop` otherwise, including when a single model reply reached its output token limit.

A streaming reply has `Content-Type: text/event-stream`. Each event is a `data:` line holding a `chat.completion.chunk`:

1. The first chunk has `delta: {"role": "assistant"}`.
2. Content chunks follow, one per committed text delta. A turn that produced no live text, such as a structured reply, sends its final text in one content chunk once it validates.
3. The finish chunk has an empty `delta`, `finish_reason`, and `usage`. `usage` is always sent here, whether or not `stream_options` asks for it.
4. `data: [DONE]` ends the stream.

An error after streaming starts sends a last chunk with `finish_reason: "error"` and the error in an `x-peen` field, then `data: [DONE]`:

```json
{"id": "chatcmpl-...", "object": "chat.completion.chunk", "created": 0, "model": "...", "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}], "x-peen": {"error": {"message": "The turn timed out.", "type": "server_error", "code": "turnTimeout"}}}
```

## Errors

Before streaming starts, errors use the OpenAI envelope and an HTTP error status:

```json
{"error": {"message": "...", "type": "...", "code": "..."}}
```

| Status | `type` | `code` | When |
| --- | --- | --- | --- |
| 400 | `invalid_request_error` | `invalidRequest` | A refused or invalid field, with the message `Invalid or unsupported field: <field>.`, or a message over a size limit, with `Invalid request.` |
| 401 | `authentication_error` | `unauthorized` | The bearer token is missing or wrong. |
| 403 | `invalid_request_error` | `permissionDenied` | Workspace policy refuses the session or workspace, or the request names the system session. |
| 404 | `invalid_request_error` | `notFound` | An unknown session, a missing workspace directory, or an unknown path under `/openai/v1`. |
| 405 | `invalid_request_error` | `methodNotAllowed` | The wrong method. The `Allow` header names the right one. |
| 409 | `invalid_request_error` | `sessionBusy` | The session already has a running turn. |
| 409 | `invalid_request_error` | `agentSpaceConflict` | The workspace runs in another agent space than the request needs. The message names both. |
| 413 | `invalid_request_error` | `requestTooLarge` | The body is over the frame limit. |
| 415 | `invalid_request_error` | `invalidRequest` | `Content-Type` is not `application/json`. |
| 500 | `server_error` | `turnFailed` | The turn failed or was cancelled. |
| 500 | `server_error` | `outputValidationFailed` | A response format was requested and no answer validated. |
| 504 | `server_error` | `turnTimeout` | The turn reached `PEEN_TURN_TIMEOUT`. |

The plain `404` while the surface is off does not use this envelope.

## Cancellation and timeouts

Disconnecting cancels this request's turn. Peen keeps the connection open for the whole turn, so keep your client's timeout long enough for the work. `PEEN_TURN_TIMEOUT` bounds every turn on this surface, which has no per-request timeout field.
