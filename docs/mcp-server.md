---
title: MCP control server
description: Connect an MCP client to Peen to run durable turns, read stored sessions, and manage files inside an allowed workspace.
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

The endpoint is stateless Streamable HTTP. Peen issues no MCP session ID, and each tool call is an independent POST that returns when the call finishes, so `sendMessage` answers once the turn has ended rather than streaming progress. Resources, prompts and sampling are not offered.

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

`sendMessage` with a `workspace` reuses that workspace's canonical session, creating it the first time. Every session of one workspace shares one agent space. Leave `agentSpace` out to use the workspace's space, or the default for a workspace with no session yet. Naming the space the workspace already uses is fine, and naming a different one is refused. `timeoutSeconds` replaces `PEEN_TURN_TIMEOUT` for that turn, longer or shorter. `outputSchema` works as it does on the [WebSocket](websocket-api.md#structured-output-and-turn-deadlines): `output` holds the validated JSON, and when every attempt fails, `outputError` says why. Images, system prompts and messages to child agents are not available here; use the WebSocket API for those. A busy session is refused rather than queued. A disconnected `sendMessage` request cancels only the turn it started.

Page sizes default to 50 and accept 1 through 200. `listSessions` filters sessions by workspace policy after paging, so a page can be short or empty while `hasMore` is true. Request the next page with `offset + limit`.

Every session selector is checked against current workspace policy, and the system session is refused. File tools share the [workspace file API](http-api.md#workspace-files) path rules and `PEEN_MAX_FILE_API_BYTES` limit. Client requests cannot choose a worker image, mount, network or capability.

A large file result carries its base64 content twice, as structured content and as the text fallback, so the client must accept a response or event of that size. Lower `PEEN_MAX_FILE_API_BYTES` when a client cannot.

## Errors

A failed tool call is not a JSON-RPC error. It is a normal result with `isError: true` and one fixed text message:

| Text | When |
| --- | --- |
| `Invalid request. Check the tool parameters.` | Both or neither of `sessionId` and `workspace`, `agentSpace` with `sessionId`, a blank `message`, a malformed session ID, an unknown `model` or `reasoningEffort`, an invalid `outputSchema`, `timeoutSeconds` outside 1 through 86400, a `limit` outside 1 through 200, a negative `offset`, invalid base64, or a file that is too large or not a regular file. |
| `The session or workspace is not allowed.` | Workspace policy refuses it, the path leaves the workspace or is not clean, or the request names the system session. |
| `The requested resource was not found.` | An unknown session, a missing workspace directory, or a missing file. |
| `this workspace runs in agent space "<current>", not "<requested>"` | `sendMessage` named an `agentSpace` other than the one the workspace's sessions use. |
| `The session already has a running turn.` | `sendMessage` named a busy session. |
| `The request was cancelled.` | The client cancelled the call. |
| `The turn timed out.` | The turn reached `timeoutSeconds` or `PEEN_TURN_TIMEOUT`. |
| `The operation could not be completed.` | Anything else, including deleting a non-empty directory. |

## Exposure

MCP clients can read stored transcripts, run model-selected commands through a turn, and write workspace files. Treat the token as control access. Transcripts and tool results are returned verbatim without secret scanning. See [Security](security.md).
