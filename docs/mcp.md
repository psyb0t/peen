---
title: "MCP servers"
description: "Give the Peen agent the tools of MCP servers, with the mcp.json format and how those tools work with hooks and allowed-tools."
nav_order: 6
parent: "The harness"
---

# MCP servers

Peen can give the agent the tools of [MCP](https://modelcontextprotocol.io) servers. A server's tools sit next to Peen's own tools: hooks, `allowed-tools`, metrics, the transcript, and durable tool phases treat them the same way. Only tools are supported. Resources, prompts, sampling, elicitation, and roots are not.

## Configuring servers

Put an `.agents/mcp.json` file in `PEEN_CONFIG_DIR`. It uses the `mcpServers` shape other agent tools use:

```json
{
  "mcpServers": {
    "github": {
      "command": "github-mcp-server",
      "args": ["stdio"],
      "envFrom": {"GITHUB_PERSONAL_ACCESS_TOKEN": "GITHUB_TOKEN"}
    },
    "docs": {
      "type": "http",
      "url": "https://docs.example.com/mcp",
      "bearerTokenEnv": "DOCS_MCP_TOKEN"
    }
  }
}
```

A server name is camelCase, at most 32 characters. Each server is a stdio server, which Peen starts as a process, or a Streamable HTTP server, which Peen reaches by URL. `type` is `stdio` or `http`, and Peen infers it from `command` or `url` when it is left out. The legacy SSE transport is not supported.

| Field | Transport | Meaning |
| --- | --- | --- |
| `command`, `args` | stdio | The server process. Peen starts it with an argument list, never a shell. |
| `env` | stdio | Extra variables, as plain values. Do not put secrets here. |
| `envFrom` | stdio | Variables the server gets from the worker, as `{"SERVER_VARIABLE": "WORKER_VARIABLE"}`. Each worker variable must be listed in `PEEN_MCP_ENV`. |
| `cwd` | stdio | Where the server runs. A relative path is relative to the workspace, which is the default. |
| `url` | http | The Streamable HTTP endpoint, `http://` or `https://`. |
| `headers` | http | Extra headers on every request. An `Authorization` header is refused, use `bearerTokenEnv`. |
| `bearerTokenEnv` | http | The worker variable whose value Peen sends as `Authorization: Bearer <value>`. It must be listed in `PEEN_MCP_ENV`. |
| `tools` | both | Keeps only these tool names. A name the server does not offer is reported as a warning. |

A file Peen cannot parse, or an entry it refuses, does not stop the other servers. It shows up as a failed server in [the status](#status). Peen refuses unknown fields, so a misspelled field fails instead of being ignored.

## Workspace servers

With `PEEN_WORKSPACE_MCP=true`, Peen also reads `<workspace>/.agents/mcp.json`, the workspace the session opened. It is off by default, because opening a session would otherwise run any command the workspace names. A workspace server never replaces a config directory server of the same name, and it may not use `envFrom` or `bearerTokenEnv`: the agent can write that file, so a workspace server that could name a variable could hand any secret to itself.

## Secrets

Secrets never go in `mcp.json`. List the variables servers may receive in `PEEN_MCP_ENV`, comma-separated, and set them in the controller's environment. Peen passes them to Docker workers, and a `control` worker inherits them. Peen removes them from the environment of every command the agent runs and every hook, the same way it removes provider keys. A stdio server starts from that same environment plus its `env` and `envFrom`, so a server never sees `PEEN_API_TOKEN` or a provider key unless you hand it one.

HTTP servers authenticate with a static bearer token. The OAuth flow the MCP specification defines for HTTP servers is not implemented.

## Where servers run

Servers run in the session's worker, in its agent space. A stdio server runs as the worker's user, with the same mounts, network, and access as `runCommand`, and in a Docker agent space it runs inside the worker container, so the command must exist in the worker image. An HTTP server is reached from the worker's network, so a Docker profile without network access cannot reach one. See [Security](security.md).

The worker starts every server when it starts, in the background, and the first tool set waits for that, at most `PEEN_MCP_START_TIMEOUT` per server. A server that fails to start, or stops later, is left out of the tool list and reported in the status. Servers stop with the worker, and a stdio server's whole process group stops with it.

At the start of every turn the worker reads `mcp.json` again and brings the servers up to date:

- When the file changed, every server restarts with the new configuration.
- A server that ran and then stopped restarts.
- A server that never started is tried again only when the file changes, so a server that cannot start does not add `PEEN_MCP_START_TIMEOUT` to every turn.
- Every running server lists its tools again, so a tool the server added or changed shows up in the next turn.

A turn's tool list does not change while the turn runs.

## Tool names and results

A tool is named `mcp__<server>__<tool>`, so hook matchers and `allowed-tools` entries written for other agent tools work. A tool name with a character providers refuse, or one that makes the name longer than 64 characters, is cleaned and cut, and gets a short hash of the original. The description says which server the tool comes from.

A tool's text goes to the model like any tool output, inside the `<toolOutput>` wrapping and under `PEEN_MAX_TOOL_RESULT_TOKENS`. The images a tool returns reach a model that takes images the same way a `readFile` image does, together in one message after the tool round, marked as data and not instructions. Every image Peen does not show, because the model takes no images, it is larger than `PEEN_MAX_IMAGE_BYTES`, it is not PNG, JPEG, GIF, or WebP data, or the call already returned `PEEN_MAX_IMAGES_PER_MESSAGE` images, is replaced by a note in the result text. Audio and binary resources become a note too. A server that fails mid-call gives the model a tool error, and the turn goes on.

## Status

At the start of every turn the worker emits an `mcp.status` event listing every configured server: its name, where it was configured, its transport, `ready` or `failed`, its tool names, why it failed, and any warnings. `GET /api/v1/session/mcp` returns the latest one with the time it was reported. See [HTTP API](http-api.md#get-apiv1sessionmcp) and [Session events](events.md).

`PEEN_MCP=false` turns MCP off entirely.
