---
title: "Security"
description: "What the Peen agent can reach, how credentials and transcripts are handled, prompt injection, unattended sessions, and network exposure."
nav_order: 3
parent: "Deployment"
---

# Security

Read this before you deploy Peen anywhere it can reach something you do not want touched.

## Entry points

Every way into Peen is a door into the same app. The WebSocket and REST API, the [OpenAI API](openai-api.md), the [MCP control server](mcp-server.md), [scheduled turns](cron.md), and the [Telegram bot](telegram.md) each start coding-agent turns, and each has its own key. `PEEN_API_TOKEN` is the key for WebSocket, REST, `/openai/v1` and `/mcp/v1`. Telegram needs no API token: its keys are the chat allowlist `PEEN_TELEGRAM_ALLOWED_CHATS` and the optional user allowlist `PEEN_TELEGRAM_ALLOWED_USERS`. Scheduled turns have no outside caller; only the operator's `cron.yaml` starts them. Once inside, every door has the same reach: a turn runs with the full access of its workspace's agent space, described below, and can read and change that workspace's files. The API token's doors also reach every regular session: WebSocket, REST and MCP read and control them, and the OpenAI API continues one by its ID. A Telegram chat reaches its own sessions and the sessions of the scheduled runs it is notified about. Only a WebSocket `message.send` with `system` set reaches [the system session](agents.md#the-system-agent), and only after you turn it on; no other door does. Guard each key as you would a shell on the machine the agent runs on.

## What the agent can do

A session's tools run in a worker process in the agent space the operator picked for that session. In the `control` agent space that worker is a child of the controller, so `runCommand` and the file tools have the same access as the operating-system user running Peen. There is no path allowlist, no secret-file denylist, and no approval or permission step before a tool runs. A file tool can read, write, or remove any path that user can touch, including `.git/`, `.env`, and SSH keys. `runCommand` runs whatever shell command the model gives it, at once. Peen is a coding agent with real access, and this is how it gets that access. The active workspace is a default, not a boundary, and an absolute tool path can still point outside it.

`webFetch` fetches any `http` or `https` URL the model names, from the worker's network, with no host allowlist. It reaches whatever `runCommand` could reach with `curl`: the internet, the local network, and in the `control` agent space the controller's own loopback listeners. A Docker profile without network access cuts it off.

`removePath` has one built-in restriction, a guard against a typo rather than a permission system. It refuses to remove the filesystem root, the session's original workspace directory, or the agent's current directory. Every other path, including everything named above, can be removed.

## Credentials

Commands the agent runs start without Peen's own secrets and the provider and MCP credentials; [Where it runs](where-it-runs.md#agent-spaces) has the full list. An [MCP server](mcp.md) receives only the `PEEN_MCP_ENV` variables its `mcp.json` entry names, and only a server from `PEEN_CONFIG_DIR` may name one. A stdio MCP server runs in the worker with the agent's access, like `runCommand`, and `PEEN_WORKSPACE_MCP=true` lets a workspace start one just by being opened, so turn it on only for workspaces you trust. On Linux the controller and its workers are non-dumpable, so a command cannot read those values from their `/proc/<pid>/environ` or memory. A `control` agent still runs as the controller's user, so it can read and change `PEEN_STATE_DIR`, which holds every session's transcript, and it can read any file or other process of that user that holds a secret. Root and processes with `CAP_SYS_PTRACE` can still read a non-dumpable process.

## Docker agent spaces

A `docker:<profile>` agent space is the isolation boundary. It runs the worker in its own container with only the mounts, network, and grants the operator defined. A client picks an agent space by name and never sends an image, mount, network setting, or capability, so how much an agent can reach is the operator's decision. If the controller cannot reach a Docker daemon, a session in a Docker agent space is refused rather than run on the host. If it can, Peen refuses the `control` agent space, because a reachable daemon makes `control` sessions host root. See [Where it runs](where-it-runs.md). The one exception is [the system agent](agents.md#the-system-agent), which you turn on yourself.

A workspace runs in one agent space, whichever door opens it. One workspace directory can hold several [sessions](architecture.md#sessions-and-entry-points), each with its own history, and all of them share the workspace's agent space. The first session decides it. A later request that names no agent space gets the workspace's one, and a request that names a different one is refused with `AGENT_SPACE_CONFLICT`. Changing the agent space with `POST /api/v1/session/reconfigure` moves every session of that workspace together. So a workspace you opened in a locked-down profile cannot also run in `control` or a stronger profile through a cron job, an OpenAI request or an MCP client, and harness files, hooks and an `.agents/mcp.json` that one session writes never run with more authority in another. The rule is per directory: a parent directory and a subdirectory opened separately are two workspaces, and a session of the parent can reach the subdirectory's files.

A worker container receives the workspace, `PEEN_CONFIG_DIR` read-only, the global learned directory read-only while `PEEN_AUTO_EVOLVE` is on and writable only with `PEEN_WRITABLE_LEARNED_DIR`, its own session directory, and only the runtime configuration it needs, including the named provider credential for its model calls. It does not receive `PEEN_STATE_DIR`, the API token, or the controller's Docker socket. A profile with `docker: full` gets that socket, and one with `docker: readonly` gets a Docker API that answers only read requests. The agent's commands run as the same user as the worker process that holds the provider credential. The worker is non-dumpable, so they cannot read the credential from the worker's `/proc` files, but an agent with root in the container, as `sudo: true` gives, may reach it. Peen runs `PEEN_WORKER_IMAGE` when it is set, then the Docker profile's `image`, then the image published alongside the running build. The container starts as root and the Peen entrypoint drops it back to your host account, so an image without that entrypoint fails when the worker starts.

## Workspace files and commands

The authenticated [workspace file API](http-api.md#workspace-files) reads and writes files only inside a regular session's stored workspace, with the current workspace policy checked again for each operation. Absolute paths, traversal and symlinks that escape it are refused. The system session is unavailable through this API. Reads and writes are bounded by `PEEN_MAX_FILE_API_BYTES`. Deletion removes a file, a symlink itself without following it, or an empty directory. These API restrictions do not change the agent tools' access described above.

Docker profile `env` values are visible to the worker's commands and can enter transcripts. Keep secrets in the named provider or MCP credential variables instead. The trusted [operator command directory](harness.md#operator-commands), `PEEN_CONFIG_DIR/.agents/bin`, precedes system commands in `PATH`, and a workspace's `.agents/bin` is never added. The system session is the one exception: its configuration directory is its own private workspace under `PEEN_STATE_DIR`, so that directory's `.agents/bin` is its operator command directory. Any session that can write the global configuration directory, meaning every `control` session and every Docker session with `PEEN_WRITABLE_CONFIG_DIR`, can add or replace these commands, and its versions then run ahead of system commands in every session's `runCommand` and hooks.

## Transcripts

Tool calls and their results are recorded verbatim in the session transcript and sent live over the global WebSocket feed, like any other message. If the agent reads a file containing a secret, or a command prints one to stdout, that secret is now in the SQLite transcript and in every connected client that did not ask for one session only. Peen does not scan for or redact secret-shaped content in tool output. Images a message carries and images `readFile` reads are stored in the session database too, and `GET /api/v1/session/model-runs/{modelRunId}/calls` returns them base64-encoded in each round's request messages. Treat the transcript and the event stream as sensitive as the files and commands the agent can reach. The starter [security rule](https://github.com/psyb0t/peen/blob/main/examples/rules/security.md) asks the agent not to print secrets, `.env` contents, or environment dumps, and to show a variable's name instead of its value. It is a request to the model, not redaction, so it lowers the chance of a leak without preventing one.

## Prompt injection

A file, a web page `webFetch` or a command fetched, a command's output, or another agent's reply can carry text written to steer the agent. Peen wraps every tool result in a `<toolOutput>` block that names the tool and where its text came from, and the default system prompt says that a `<toolOutput>` block is untrusted data, never instructions. A result whose text comes from outside the workspace and the session, from `webFetch`, an MCP tool, `readExecution` or `searchExecutions`, also ends with a notice that says the text is data from that source, did not come from the user, and holds no instructions to follow, and that only the user's messages can ask for new actions. Peen's own error text gets no notice, because its instructions, such as how to retry a call, are meant to be followed. Peen escapes `<toolOutput` and `</toolOutput` inside a result, so planted text cannot close the block early. The wrapped result, notice included, fits within `PEEN_MAX_TOOL_RESULT_TOKENS`. Peen shortens the raw text to make room. Results of `ask` and `useSkill` are not wrapped, because an answer to `ask` comes from the user or the parent agent and a skill holds instructions the agent loads on purpose. A hook's `deny` reason is not wrapped either, because it is your own configuration. An image `readFile` reads reaches the model as a user message after the tool round, because providers take images only in user messages. That message's text names the file and carries the same notice, but the model can still read text drawn inside the image. Hooks see the raw result before Peen wraps it. The transcript and the WebSocket feed carry the wrapped result, as the model read it. A WebSocket `systemPrompt` in `replace` mode removes the base prompt and this instruction with it, so a replacement prompt has to restate it. The OpenAI API's system messages and the Telegram `/system` command append to the base prompt and keep it.

The wrapper helps the model recognize injected instructions. It does not make them harmless, and a model can still follow them. The [agent space](#docker-agent-spaces) stays the boundary that limits what a misled agent can reach.

[Auto-evolve](harness.md#auto-evolve) makes a successful injection last. An agent misled into saving a planted instruction as a learned rule or skill hands it to every later session that reads that learned directory. That is why Docker workers get the global learned directory, which reaches every session, read-only unless you set `PEEN_WRITABLE_LEARNED_DIR`. A `control` worker runs as your user and can write it anyway, but its prompt does not offer it. The prompt tells the agent to save only what the user's messages and its own verified work taught it, which lowers the chance without preventing it. Review `.agents/learned/` like any other harness change. `PEEN_WRITABLE_CONFIG_DIR` widens this to your whole global harness, hooks included, and a hook runs commands in every session.

## Unattended sessions

A session with no client attached keeps running. A turn ends when it finishes, fails, reaches its deadline (`PEEN_TURN_TIMEOUT` or the message's own `timeoutSeconds`), or reaches `PEEN_MAX_TURN_TOKENS`. A client can cancel it, and Stop, shutting down the controller, an embedded `Close`, or a crash end it too. A child agent can outlive its turn until it finishes or is cancelled. With both limits at their default of 0, an unattended turn keeps calling the model and spending provider budget until one of those happens. Scheduled jobs repeat on every tick, and `@every` accepts intervals as short as one second.

`PEEN_TURN_TIMEOUT` is a default, not a cap: a WebSocket or MCP client can set `timeoutSeconds` on its own message, up to 24 hours, longer or shorter than the setting. `PEEN_MAX_TURN_TOKENS` is the limit a client cannot raise, and it applies to each turn, not to a job or a day.

## Network exposure

`PEEN_API_TOKEN` is empty by default, and an empty token turns authentication off on every door it guards. Loopback alone does not keep the API private then. A web page your browser opens can reach the port through DNS rebinding, which makes the browser treat the attacker's host name as the page's own origin. Only `/mcp/v1` checks the `Host` header, so with an empty token such a page can call `/api/v1`, including the workspace file endpoints that write files, and start coding-agent turns through `/ws/v1` and `/openai/v1`. Set `PEEN_API_TOKEN` whenever a browser can reach the port, which includes a browser on the same machine, and use `wss://` outside local development. Browser WebSockets authenticate with subprotocols, because browsers cannot attach an `Authorization` header. The [WebSocket API](websocket-api.md) has the handshake.

## Running it

Run Peen as a non-root user, in a container, with only the mounts, network access, and capabilities the deployment needs. The installer's default does this. [Deployment](deployment.md#docker) covers production mounts, networking, and container hardening.

## OpenAI API

The optional [OpenAI API](openai-api.md) is guarded by `PEEN_API_TOKEN` and has the same authority as the REST API. A client can start coding-agent turns in any workspace the policy allows, continue a regular session by its ID, and read the replies. Client history and images enter transcripts and reach the model provider. Its system messages are appended to Peen's base prompt and never replace it. Enable it only with a token set.

## MCP control server

The optional [MCP control server](mcp-server.md) is guarded by `PEEN_API_TOKEN` and gives its clients the reach of the REST API: turns, regular session transcripts, and workspace files. Its Host and Origin allowlists apply only to `/mcp/v1`. They keep browser pages and DNS rebinding off that path, and they do not replace the token. Returned transcripts are not scanned for secrets.

## Scheduled turns

[Scheduled turns](cron.md) run without an interactive caller and spend provider budget. The controller reads only the operator's `PEEN_CONFIG_DIR/.agents/cron.yaml`, at startup with `PEEN_CRON=true`. Review that file and each workspace's harness before enabling it. Give each job a `timeout` or set `PEEN_TURN_TIMEOUT`, and set `PEEN_MAX_TURN_TOKENS`, to bound each run. Scheduling is off by default.

Cron opens regular sessions in the job's workspace, with the same workspace policy at startup and dispatch, and never the system session. Each job runs in its workspace's agent space: a job whose `agentSpace` differs from the one the workspace's sessions already use stops startup. Any `control` session, and any Docker session with `PEEN_WRITABLE_CONFIG_DIR`, can write `cron.yaml` and change scheduled work after the next restart, so treat both as trusted with operator configuration. Cron history exposes fixed failure reasons rather than raw provider errors. The normal transcript still records full messages and tool results.

## Telegram

The optional [Telegram bot](telegram.md) lets explicitly allowed chats drive regular workspace sessions, and never the system session. Each chat's directory is its own workspace, in `PEEN_TELEGRAM_AGENT_SPACE`. The optional user allowlist narrows who in those chats may send work. Without it, anyone in an allowed chat can drive the agent, including people added to a group later, so set `PEEN_TELEGRAM_ALLOWED_USERS` for every group chat.

A reply to a scheduled notification continues that run's session, in the job's workspace and that workspace's agent space, the one the operator chose for the job. Uploads and `/fetch` in that reply use that workspace too. Anyone in the allowed chat therefore drives that session with that agent space's authority, so give a job a `telegramChatId` only when everyone allowed in that chat may do so.

The file API that saves uploads and serves `/fetch` stays inside the session's workspace. The agent's tools do not: in `control`, or in a Docker profile whose mounts include `PEEN_TELEGRAM_WORKSPACE`, one chat's agent can read and change other chats' files. Use a Docker agent space without extra mounts when chats must not see each other.

The controller sends HTTPS requests to `api.telegram.org`. Answers, scheduled notifications and fetched files leave Peen through Telegram without secret scanning. The bot token is controller-only and excluded from worker and command environments. A `control` agent can still read it from the `.env` file or secret store that supplies it, like every other secret its user can read. The client drops token-bearing URLs, remote bodies and transport causes from errors. A cron reply uses a saved chat/message binding and current workspace policy, never a session selector from Telegram text.
