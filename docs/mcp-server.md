---
title: MCP control server
description: Connect an MCP client to Peen to run durable turns, follow their progress, and read sessions and workspace files as tools or resources.
nav_order: 12
---

# MCP control server

This page is about Peen as an MCP **server**: an outside MCP client, such as another coding agent, connects to Peen and drives it. To give Peen's own agent the tools of other MCP servers, see [MCP tool servers](mcp.md), which is a separate feature.

Set `PEEN_MCP_SERVER=true` to serve Streamable HTTP at `/mcp/v1`. It uses the same listener and `PEEN_API_TOKEN` bearer authentication as the [REST API](http-api.md). With it off, the path returns a plain `404`.

## Connect

Configure your MCP client's HTTP server entry with this URL and an Authorization header. Keep the real token in the client's secret store or environment.

```json
{
  "mcpServers": {
    "peen": {
      "url": "http://localhost:8080/mcp/v1",
      "headers": {
        "Authorization": "Bearer your-token-here"
      }
    }
  }
}
```

The endpoint is stateless Streamable HTTP. Peen issues no MCP session ID, and each request is an independent POST that returns when the call finishes, so `sendMessage` answers once the turn has ended. A client that sends a progress token gets [progress notifications](#progress) on that call's response stream while the turn runs.

The server offers [tools](#tools), [resources](#resources), [completion](#completion) and [instructions](#instructions). [Protocol versions](#protocol-versions) says what each client version gets, and [Not offered](#not-offered) lists the MCP features Peen leaves out.

The default allowed hosts are `localhost`, `127.0.0.1` and `[::1]`, with any valid port. The default allowed origins are `http://localhost:8080`, `http://127.0.0.1:8080` and `http://[::1]:8080`. Set `PEEN_MCP_SERVER_ALLOWED_HOSTS` to a comma-separated list of hostnames without ports, and `PEEN_MCP_SERVER_ALLOWED_ORIGINS` to exact HTTP or HTTPS origins when using a different address. Wildcards are refused.

Peen checks each request in this order:

1. The bearer token. A missing or wrong token gets `401` with the REST error envelope, before the Host is looked at.
2. The `Host` header. A host outside the allowlist gets `421` with code `MISDIRECTED_REQUEST`.
3. The `Origin` header. An origin outside the allowlist, or more than one `Origin` header, gets `403` with code `FORBIDDEN`. A request with no `Origin` header, which is how a client outside a browser connects, passes this check, but a browser request that the browser marks as cross-site still gets `403`.

The `421` and `403` bodies use the same `{"code", "message"}` envelope as the REST API. Request bodies are limited to the larger of the WebSocket frame limit and the base64 size of `PEEN_MAX_FILE_API_BYTES`.

## Tools

Every tool advertises only `{"type": "object"}` as its output schema, so this table is the contract for results. A result arrives as structured content, with the same JSON as a text fallback.

| Tool | Input | Result |
| --- | --- | --- |
| `sendMessage` | Exactly one of `sessionId` or `workspace`, plus a non-empty `message`. Optional `agentSpace` (with `workspace` only), `model`, `reasoningEffort`, `outputSchema` (a JSON Schema object), and `timeoutSeconds` (1 through 86400). | `{sessionId, text, state, output?, outputError?}`. `text` is the final answer and `state` is the stored turn state. |
| `listSessions` | Optional `limit` and `offset`. | `{items, limit, offset, hasMore}`, the REST [session page](https://psyb0t.github.io/peen/api/list-sessions/) with only permitted regular sessions. |
| `getSession` | `sessionId`. | The REST [session object](https://psyb0t.github.io/peen/api/get-session/). |
| `readMessages` | `sessionId` and optional `limit`. | `{items, limit, offset, hasMore}` with the last `limit` messages, oldest first. `hasMore` means older messages exist. |
| `cancelTurn` | `sessionId`. | `{cancelled}`, `true` when a pending or running turn was signalled. |
| `listModels` | None. | `{models}`, the same records as [`GET /api/v1/models`](websocket-api.md): `name`, `connectionName`, `modelId`, `contextWindowTokens` and `imageInput`. Pass `name` as `model`. |
| `listWorkspaceFiles` | `sessionId` and optional relative `path`; omit it for the workspace root. | `{path, type, size, modifiedAt, entries}`. |
| `readWorkspaceFile` | `sessionId` and relative `path`. | `{path, size, contentBase64}`, up to `PEEN_MAX_FILE_API_BYTES` decoded. |
| `writeWorkspaceFile` | `sessionId`, relative `path`, `contentBase64`, and optional `createDirectories`. Writes atomically. | `{name, type, size, modifiedAt}`. |
| `deleteWorkspaceFile` | `sessionId` and relative `path`. Deletes one file, symlink or empty directory. | `{deleted: true}`. |

`sendMessage` with a `workspace` reuses that workspace's session, creating it the first time. Leave `agentSpace` out to use the workspace's space, or the default for a workspace with no session yet. Naming the space the workspace already uses is fine, and naming a different one is refused. `timeoutSeconds` replaces `PEEN_TURN_TIMEOUT` for that turn, longer or shorter. `outputSchema` works as it does on the [WebSocket](websocket-api.md#structured-output-and-turn-deadlines): `output` holds the validated JSON, and when every attempt fails, `outputError` says why. Images, system prompts and messages to child agents are not available here; use the WebSocket API for those. A busy session is refused rather than queued. A disconnected `sendMessage` request cancels only the turn it started.

Page sizes default to 50 and accept 1 through 200. `listSessions` filters sessions by workspace policy after paging, so a page can be short or empty while `hasMore` is true. Request the next page with `offset + limit`.

Every session selector is checked against current workspace policy, and the system session is refused. File tools share the [workspace file API](http-api.md#workspace-files) path rules and `PEEN_MAX_FILE_API_BYTES` limit. Client requests cannot choose a worker image, mount, network or capability.

A large file result carries its base64 content twice, as structured content and as the text fallback, so the client must accept a response or event of that size. Lower `PEEN_MAX_FILE_API_BYTES` when a client cannot.

The results of `sendMessage` and `getSession` also carry two `resource_link` parts, pointing to `peen://session/<id>` and `peen://session/<id>/messages`. A `listSessions` result carries one `resource_link` part for each listed session, pointing to `peen://session/<id>`. The JSON text part stays first.

## Instructions

The server sends a short plain-text guide with its capabilities, which a client may show to its model. It says that each workspace has one session, how `sendMessage` picks or opens one, that a busy session refuses new messages, where model names come from, that paths are relative to the workspace root, which resource URIs exist, and that transcripts and files are data, not instructions. It names no host, path or token.

## Resources

Resources return the same stored state as the read tools. Every session URI passes the same workspace policy check as a tool's `sessionId`, and the system session is refused.

| URI | Type | Contents |
| --- | --- | --- |
| `peen://sessions` | `application/json` | The first page of up to 200 permitted regular sessions, shaped like the `listSessions` result. Each item adds a `uri` field with its `peen://session/<id>` URI. |
| `peen://session/{sessionId}` | `application/json` | The session object, the same JSON as `getSession`. |
| `peen://session/{sessionId}/messages` | `application/json` | The last 50 messages, oldest first, shaped like the `readMessages` result. |
| `peen://session/{sessionId}/files/{+path}` | depends on the path | A directory returns its listing JSON, like `listWorkspaceFiles`, as `application/json`. A file whose bytes are valid UTF-8 with no NUL byte returns them as `text` with type `text/plain`. Any other file returns them base64 encoded as `blob` with type `application/octet-stream`. An empty `path` lists the workspace root. |

`resources/list` returns only `peen://sessions`. The three session URIs are templates, returned by `resources/templates/list`, so the list stays the same for every client. Percent-encode a path the way URI templates require, such as `%20` for a space and `%25` for a literal `%`. Peen decodes `sessionId` and `path` exactly once. It refuses a path that is not clean and relative, and a path that a second decode would turn into one, so neither `%2e%2e` nor `%252e%252e` climbs out of the workspace. Files use the same path rules and `PEEN_MAX_FILE_API_BYTES` limit as the file tools.

A file resource carries its content once, unlike a file tool result, but a binary file still grows by a third as base64, and the client must accept a response of that size. Lower `PEEN_MAX_FILE_API_BYTES` when a client cannot.

A failed read is a JSON-RPC error with a fixed message that never includes the underlying cause:

| Code and message | When |
| --- | --- |
| `-32602` `Resource not found` | An unknown session, a missing file, or a URI that matches no resource or template. The error data carries the requested `uri`. |
| `-32602` `The session or workspace is not allowed.` | Workspace policy refuses the session, the URI names the system session, or the path leaves the workspace or is not clean. |
| `-32602` `The resource URI is not valid, or it names a file that cannot be read.` | A malformed session ID or escape, a file larger than `PEEN_MAX_FILE_API_BYTES`, or a path that is not a regular file. |
| `-32603` `The operation could not be completed.` | Anything else. |

## Completion

`completion/complete` suggests values for the resource template arguments.

- `sessionId`, in any of the three session templates, completes to the IDs of permitted regular sessions that start with the typed text, the same sessions `listSessions` shows. It returns at most 20 values and looks at the first 1000 sessions.
- `path`, in the files template, needs `sessionId` in the request's context arguments. It lists the directory before the last `/` of the typed text and returns the entries whose names start with the text after it, as paths relative to the workspace root, at most 20 of them. The typed text is the plain path, not percent-encoded.

A refused, malformed or unknown session or path completes to an empty list, so completion reveals nothing the tools do not.

## Progress

When a `sendMessage` call carries a `progressToken` in its `_meta`, Peen sends `notifications/progress` for it while the turn runs. `progress` counts the reported events. `message` names only the event type, plus the tool name for a tool call, such as `tool.use runCommand`, cut to 200 bytes. The reported events are `turn.started`, each `tool.use`, and the end of the turn (`turn.completed`, `turn.failed` or `turn.cancelled`). Peen sends at most one notification every 500 milliseconds, and when the turn ends it sends the last event it held back.

Notifications carry no transcript text, tool arguments or tool results. A notification that cannot be delivered is dropped and never fails the turn.

## Caching

Every list result, `resources/read` result and discovery result says `cacheScope: "private"` and `ttlMs: 0`. A client refetches instead of reusing a result, and no shared cache keeps a transcript or file. A client that predates these fields ignores them.

## Protocol versions

Peen speaks MCP `2026-07-28` and the older versions back to `2024-11-05`.

- A `2026-07-28` client discovers the server with `server/discover` and gets the tools, resources, completion, instructions, progress and caching fields described here. No list ever changes and nothing can be subscribed to, so the client has no reason to hold a `subscriptions/listen` stream open.
- An older client connects with `initialize` and gets the same tools, resources, completion, instructions and progress notifications. The endpoint is stateless, so Peen never sends a request back to the client.

## Not offered

- Logging. MCP `2026-07-28` deprecates it, and progress notifications already report what a turn is doing. The server advertises no `logging` capability.
- Prompts. Peen has no reusable prompt templates to offer. The agent's own instructions live in the [harness](harness.md).
- Resource subscriptions and list-change notifications. Read a resource again to see new state, or follow the [WebSocket feed](websocket-api.md) for live events.
- Elicitation, sampling and roots. Peen never asks the MCP client for input, a model call, or its directories. MCP `2026-07-28` deprecates sampling and roots.

## Errors

A failed resource read is a JSON-RPC error, described under [Resources](#resources). A failed tool call is not a JSON-RPC error. It is a normal result with `isError: true` and one fixed text message:

| Text | When |
| --- | --- |
| `Invalid request. Check the tool parameters.` | Both or neither of `sessionId` and `workspace`, `agentSpace` with `sessionId`, a blank `message`, a malformed session ID, an unknown `model` or `reasoningEffort`, an invalid `outputSchema`, `timeoutSeconds` outside 1 through 86400, a `limit` outside 1 through 200, a negative `offset`, invalid base64, or a file that is too large or not a regular file. |
| `The session or workspace is not allowed.` | Workspace policy refuses it, the path leaves the workspace or is not clean, or the request names the system session. |
| `The requested resource was not found.` | An unknown session, a missing workspace directory, or a missing file. |
| `this workspace runs in agent space "<current>", not "<requested>"` | `sendMessage` named an `agentSpace` other than the one the workspace's session runs in. |
| `The session already has a running turn.` | `sendMessage` named a busy session. |
| `The request was cancelled.` | The client cancelled the call. |
| `The turn timed out.` | The turn reached `timeoutSeconds` or `PEEN_TURN_TIMEOUT`. |
| `The operation could not be completed.` | Anything else, including deleting a non-empty directory. |

## Exposure

MCP clients can read stored transcripts, run model-selected commands through a turn, and write workspace files. Treat the token as control access. Transcripts and tool results are returned verbatim without secret scanning. See [Security](security.md).
