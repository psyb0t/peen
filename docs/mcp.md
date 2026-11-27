---
title: "MCP tool servers"
description: "Connect the Peen agent to MCP servers: the mcp.json format, tools, instructions, resources, prompts, elicitation, and what Peen leaves out."
nav_order: 6
parent: "The harness"
---

# MCP tool servers

This page is about Peen as an MCP **client**: Peen connects its agent to [MCP](https://modelcontextprotocol.io) servers. To let an outside MCP client operate Peen itself, see the [MCP control server](mcp-server.md), which is a separate feature.

Peen speaks MCP 2026-07-28 and falls back to 2025-11-25 for older servers. From each server it takes:

- its [tools](#tool-names-and-results), which sit next to Peen's own tools: hooks, `allowed-tools`, metrics, the transcript, and durable tool phases treat them the same way;
- its [instructions](#instructions), which go into the system prompt;
- its [resources](#resources), which the agent lists and reads with `listMcpResources` and `readMcpResource`;
- its [prompts](#prompts), which a user invokes with `/mcp:<server>:<prompt>` and the agent reads with `listMcpPrompts` and `getMcpPrompt`;
- its [questions for the user](#elicitation), through the same flow as the agent's `ask` tool.

Peen does not offer roots, sampling, or logging, which MCP 2026-07-28 deprecates, and does not support the legacy HTTP+SSE transport. See [What Peen leaves out](#what-peen-leaves-out).

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
      "bearerTokenEnv": "DOCS_MCP_TOKEN",
      "prompts": false
    }
  }
}
```

A server name is camelCase, at most 32 characters. Each server is a stdio server, which Peen starts as a process, or a Streamable HTTP server, which Peen reaches by URL. `type` is `stdio` or `http`, and Peen infers it from `command` or `url` when it is left out.

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
| `instructions` | both | `false` keeps the server's instructions out of the system prompt. On by default. |
| `resources` | both | `false` keeps the server out of `listMcpResources` and `readMcpResource`. On by default. |
| `prompts` | both | `false` hides the server's prompts from the status, from `/mcp:<server>:<prompt>`, and from `listMcpPrompts` and `getMcpPrompt`. On by default. |

A file Peen cannot parse, or an entry it refuses, does not stop the other servers. It shows up as a failed server in [the status](#status). Peen refuses unknown fields, so a misspelled field fails instead of being ignored, and a `roots` field from another tool's configuration is refused too.

## Workspace servers

With `PEEN_WORKSPACE_MCP=true`, Peen also reads `<workspace>/.agents/mcp.json`, the workspace the session opened. It is off by default, because opening a session would otherwise run any command the workspace names. A workspace server never replaces a config directory server of the same name, and it may not use `envFrom` or `bearerTokenEnv`: the agent can write that file, so a workspace server that could name a variable could hand any secret to itself.

The agent can also write a workspace server's instructions, resources, and prompts once it can write the workspace. Their blocks say `source="workspace"`, and `"instructions": false` keeps a server's text out of the prompt.

## Secrets

Secrets never go in `mcp.json`. List the variables servers may receive in `PEEN_MCP_ENV`, comma-separated, and set them in the controller's environment. Peen passes them to Docker workers, and a `control` worker inherits them. Peen removes them from the environment of every command the agent runs and every hook, the same way it removes provider keys. A stdio server starts from that same environment plus its `env` and `envFrom`, so a server never sees `PEEN_API_TOKEN` or a provider key unless you hand it one.

HTTP servers authenticate with a static bearer token. The OAuth flow the MCP specification defines for HTTP servers is not implemented.

## Where servers run

Servers run in the session's worker, in its agent space. A stdio server runs as the worker's user, with the same mounts, network, and access as `runCommand`, and in a Docker agent space it runs inside the worker container, so the command must exist in the worker image. An HTTP server is reached from the worker's network, so a Docker profile without network access cannot reach one. See [Security](security.md).

The worker starts every server when it starts, in the background, and the first turn waits for that, at most `PEEN_MCP_START_TIMEOUT` per server. A server that fails to start, or stops later, is left out of the tool list and reported in the status. Servers stop with the worker, and a stdio server's whole process group stops with it.

At the start of every turn, before the system prompt is built, the worker reads `mcp.json` again and brings the servers up to date:

- When the file changed, every server restarts with the new configuration.
- A server that ran and then stopped restarts.
- A server that never started is tried again only when the file changes, so a server that cannot start does not add `PEEN_MCP_START_TIMEOUT` to every turn.
- Every running server lists its tools and prompts again, so a change the server made shows up in the next turn. A server that says its lists changed only clears Peen's cached copy; the new lists still apply from the next turn.

A turn's tools and system prompt do not change while the turn runs.

## Tool names and results

A tool is named `mcp__<server>__<tool>`, so hook matchers and `allowed-tools` entries written for other agent tools work. A tool name with a character providers refuse, or one that makes the name longer than 64 characters, is cleaned and cut, and gets a short hash of the original. The description says which server the tool comes from. A description longer than 2048 bytes is cut there, with a `<note kind="descriptionCut">` after it, because every tool description travels with every model request. Peen logs a warning with `reason=tool_count_high` when an agent ends up with more than 30 tools.

A tool's text goes to the model like any tool output, inside the `<toolOutput>` wrapping and under `PEEN_MAX_TOOL_RESULT_TOKENS`. The images a tool returns reach a model that takes images the same way a `readFile` image does, together in one message after the tool round, marked as data and not instructions. Every image Peen does not show, because the model takes no images, it is larger than `PEEN_MAX_IMAGE_BYTES`, it is not PNG, JPEG, GIF, or WebP data, or the call already returned `PEEN_MAX_IMAGES_PER_MESSAGE` images, is replaced by a note in the result text. Audio becomes a note too. A server that fails mid-call gives the model a tool error, and the turn goes on.

A resource link in a tool result reaches the model as `[resource link: <uri> name="..." mimeType="..." size=N]`, which `readMcpResource` can read. An embedded text resource gets a `[embedded resource <uri> (<type>)]` line before its text, an embedded PNG, JPEG, GIF, or WebP image is shown like any other image, and any other embedded binary resource is described by type and size.

## Instructions

A server can send instructions on how to use its tools, resources, and prompts. Peen puts each running server's instructions into the system prompt of the agent and of every child agent, after the skill and agent lists and before `<environment>`, in configuration order:

```text
<mcpInstructions server="github" source="config">
...the server's text...
</mcpInstructions>
```

- `PEEN_MCP_MAX_INSTRUCTIONS_BYTES`, `4096` by default, bounds each server's text. Longer text is cut at a character boundary and ends with `<note kind="instructionsCut">`. `0` leaves every server's instructions out.
- `"instructions": false` in an entry leaves that server's out.
- The text is the server's: Peen escapes any `mcpInstructions`, `mcpPrompt`, `promptMessage`, or `note` tag in it, so it cannot end the block or pose as a Peen notice, and the system prompt tells the model the server wrote it and that it covers only using that server.
- The blocks count toward `PEEN_MAX_SYSTEM_PROMPT_PERCENT`. A refused prompt names a server's block as `mcp server <name>` among its largest sections.
- The instructions change only when a server restarts with different text, at a turn boundary, so the cached prompt prefix stays the same between turns.

## Resources

When a running server offers resources and its entry leaves `resources` on, the agent gets two tools:

| Tool | Arguments | Returns |
| --- | --- | --- |
| `listMcpResources` | `server` (optional), `cursor`, `templatesCursor` | Each server's first page of resources (`uri`, `name`, `title`, `description`, `mimeType`, `size`) and resource templates (`uriTemplate`, ...), with `nextCursor` and `nextTemplatesCursor` when there is more. A cursor needs its `server`. |
| `readMcpResource` | `server`, `uri` | The resource's text, each item after a `[resource <uri> (<type>)]` line. Images reach a model that takes images like an MCP tool's images, and other binary contents are described by type and size. |

The `server` argument lists only the servers that offer resources when the turn's tools are built. Descriptions and titles are cut to 256 bytes. Both results are `<toolOutput>` blocks with the notice that the text is data from an MCP server, under `PEEN_MAX_TOOL_RESULT_TOKENS`. A resource read is bounded at 8 MiB, the elelem default, before it is cut to fit. Peen passes a URI only to the server that owns it and never fetches it itself.

## Prompts

A prompt is a reusable message a server fills in from named arguments. The user and the agent can both use one.

**The user** invokes a prompt with a message whose first line is:

```text
/mcp:<server>:<prompt> name=value other="a value with spaces"
```

A value with spaces, quotes, or `=` is a double-quoted JSON string. The lines after the first are the user's own text. Peen asks the server for the prompt before the message is stored and replaces the first line with the result:

```text
<mcpPrompt server="docs" name="review">
<promptMessage role="user">
...the server's text...
</promptMessage>
</mcpPrompt>

...the rest of the message...
```

The expanded text is the message the turn stores, the client sees in `userMessage.created`, hooks receive, and the model reads, so a replay never asks the server again. It works the same for a message that starts a turn and one that joins a running turn, from every entry point. The server's text is escaped like instructions, and a `:skill` reference in it loads nothing: only the user's own text can name skills. An image in a prompt is described, not shown.

The message is refused with `VALIDATION_FAILED`, before anything is stored, when no running server by that name offers prompts, the server has no such prompt or refuses the arguments, or the expanded message is over `PEEN_MAX_MESSAGE_BYTES`. A message that starts with `/mcp:` is always read as a prompt, so put any other text first if you mean it literally. The arguments are sent to the server, so do not put secrets in them unless you trust that server.

[The status](#status) lists each server's prompts with their arguments, so a client can offer a picker.

**The agent** gets `listMcpPrompts` (`server`, `cursor`) and `getMcpPrompt` (`server`, `name`, `arguments` as string values) when a running server offers prompts. `getMcpPrompt` returns the same `<mcpPrompt>` block inside a `<toolOutput>` block, and its images reach a model that takes images.

## Elicitation

A server can ask the user a question while one of its tool calls, resource reads, or prompts runs. Peen advertises form elicitation and routes a question through the same flow as the agent's `ask` tool:

1. Peen sends a `question.asked` frame whose text names the server, quotes the server's message between `---` lines, and lists the fields the form asks for with their types, whether they are required, and their choices. The server's message is cut to 4096 bytes. It is the server's text, not Peen's or the agent's.
2. The user's next message is the answer: a JSON object of the fields, the bare value when the form has one field, `decline` to refuse, or `cancel` to skip. Peen checks the answer against the form: unknown fields, missing required fields, types, choices, lengths, and number bounds. A reply that does not fit is asked again with the reason, up to three times, and then declined.
3. A `question.answered` frame follows, and Peen sends the answer to the server.

The user's reply also joins the conversation like any message sent while a turn runs, and the tool result tells the model that a question was asked and the reply answers the server, not the model. Do not answer with a secret: a form is not a safe place for one, and the reply reaches both the server and the model.

Peen declines a question, without asking anyone, when no person can answer it:

- no turn is running, for example a question that arrives between turns;
- the turn came from a caller that waits for its own result: the [OpenAI API](openai-api.md), the [MCP control server](mcp-server.md), [scheduled turns](cron.md), or the [Telegram bot](telegram.md), or from a session event;
- the server asks the user to open a URL (URL mode), which Peen does not offer, so a link from a server is never presented as a Peen request.

A question nobody answers within `PEEN_ASK_TIMEOUT`, or before the turn ends, is cancelled. A question pending when the worker restarts is lost with the call that was waiting for it.

## Status

At the start of every turn the worker emits an `mcp.status` event listing every configured server: its name, where it was configured, its transport, `ready` or `failed`, its tool names, why it failed, and any warnings. A ready server also reports:

- `protocolVersion` and `serverInfo` (name, title, version, each cut to 128 bytes);
- `features`: the capabilities it advertised, from `tools`, `resources`, `resourceSubscribe`, `prompts`, and `completions`;
- `instructionsBytes`, `instructionsIncluded`, and `instructionsCut`;
- `prompts`: its first 50 prompts with their arguments, unless its entry turns prompts off.

With no server configured, the list is empty, so the report clears on the first turn after you remove `mcp.json`. [`GET /api/v1/session/mcp`](https://psyb0t.github.io/peen/api/get-session-mcp-status/) returns the latest one with the time it was reported. See [Session events](events.md). Generated WebSocket clients refuse fields they do not know, so regenerate them from `api/asyncapi.yaml` before they read a status from this version.

## What Peen leaves out

- **Roots, sampling, and logging.** MCP 2026-07-28 deprecates them (SEP-2577), and Peen never advertises them. A server that needs roots should take paths as tool arguments; a stdio server already runs in the workspace. Sampling would let a server spend the operator's provider keys outside the turn's limits. A server's log messages are not requested; its stderr goes to the worker's debug log.
- **The HTTP+SSE transport.** Only Streamable HTTP is supported for remote servers.
- **URL elicitation and OAuth.** See [Elicitation](#elicitation) and [Secrets](#secrets).
- **Resource subscriptions and completion.** Peen reports when a server offers them but does not use them.

`PEEN_MCP=false` turns MCP off entirely.
