---
title: Troubleshooting
description: Fix Peen when it refuses to start, a port or provider key is wrong, a harness file fails to load, or the API answers with an error code.
nav_order: 17
---

# Troubleshooting

Start with these two commands. `peen doctor` checks the installation and names the fix for each failure. `peen logs` shows the last controller log lines, and `peen logs -f` follows them.

```bash
peen doctor
peen logs 200
```

## Peen refuses to start

Startup names the variable and the value it rejected, and `peen start` prints those lines. A bad value stops startup on purpose, so a mistake fails here and not in the middle of a turn.

| What you see | Cause | Fix |
| --- | --- | --- |
| `provider key <NAME> is not set` | An upstream's `apiKeyEnv` names a variable with no value. | `peen config set <NAME> <your key>` |
| `<address> is already in use by another program` | Another program holds the listen port. | Pick another with `peen setup --listen`. |
| `bind: address already in use` in the logs | The API and `PEEN_METRICS_LISTEN_ADDRESS` share a port, or another program holds it. | Give them different ports. |
| `the agent would be host root` | The host control space with the `control` agent space, while your user can reach a Docker daemon. | `peen setup --agent-space docker`. See [the Docker socket rule](where-it-runs.md#the-docker-socket-rule). |
| `Docker does not answer 'docker info'` | The daemon is down, or your user cannot reach it. | Start Docker, or add your user to its group. |
| `image <name> is missing` | The pinned image is not on this machine. | `peen setup` |
| `has mode <n>, want <m>` | A configuration file or directory is too open. | Run the `chmod` the message gives. |
| `produces a <n> byte socket path, over the 107 byte limit` | The worker socket directory path is too long. | Set `PEEN_WORKER_SOCKET_DIR` to a [shorter directory](configuration.md#worker-sockets). |
| A database schema error that names files to delete | Before 1.0, Peen refuses a database from an earlier schema. | Follow [durable state and mounts](deployment.md#durable-state-and-mounts), which lists the files. |
| The config and state directories overlap | `PEEN_CONFIG_DIR` and `PEEN_STATE_DIR` are equal, or one is inside the other. | Point them at [separate directories](configuration.md#directories). |
| The metrics address is refused | `PEEN_METRICS_LISTEN_ADDRESS` is a wildcard or non-loopback address. | Use a loopback address such as `127.0.0.1:9090`. See [metrics](configuration.md#metrics). |
| A Docker profile or agent space value is refused | A bad value in `PEEN_DOCKER_PROFILES` or `PEEN_DEFAULT_AGENT_SPACE`. | See [why startup was refused](where-it-runs.md#why-startup-was-refused). |
| `PEEN_TELEGRAM_TOKEN is missing or invalid`, `PEEN_TELEGRAM_ALLOWED_CHATS requires nonzero chat IDs`, or `PEEN_TELEGRAM_WORKSPACE must be absolute` | `PEEN_TELEGRAM=true` with incomplete settings. | Set all three. See [Telegram](configuration.md#telegram). |
| `load cron jobs`, with the job and field it refused | `.agents/cron.yaml` is invalid, or a job names an agent space or Telegram chat the deployment does not allow. | Fix the job and restart. See [scheduled turns](cron.md). |
| A cron job's agent space conflicts with its workspace, naming the job, the workspace, and both agent spaces | The job's `agentSpace` differs from the space the workspace session already uses. | Give the job the workspace's agent space, or leave `agentSpace` out, or move the workspace with `POST /api/v1/session/reconfigure`. |
| `PEEN_OPENAI_WORKSPACE must be absolute` | The OpenAI fallback workspace is a relative path. | Use an absolute path inside the workspace root. |
| `PEEN_MCP_SERVER_ALLOWED_HOSTS is invalid` or `PEEN_MCP_SERVER_ALLOWED_ORIGINS is invalid` | A wildcard, a port in a host, or an origin that is not an exact HTTP or HTTPS origin. | See [MCP control server](mcp-server.md#connect). |

## The controller does not answer

`peen doctor` reports `the controller is not running` until you run `peen start`. When it reports that `/healthz` does not answer, read `peen logs`, because the error is in there.

When `GET /api/v1/models` answers with an error, check `PEEN_API_TOKEN` and the provider settings with `peen config edit`.

## An optional endpoint answers 404, 421, or 403

`/openai/v1` and `/mcp/v1` answer a plain `404` while `PEEN_OPENAI_API` or `PEEN_MCP_SERVER` is off. Turn the setting on and restart. `/mcp/v1` answers `421` when the request's `Host` is not in `PEEN_MCP_SERVER_ALLOWED_HOSTS`, and `403` when its `Origin` is not in `PEEN_MCP_SERVER_ALLOWED_ORIGINS`. Add the address the client uses. See [MCP control server](mcp-server.md#connect).

## The Telegram bot never answers

Peen ignores messages from chats and users outside `PEEN_TELEGRAM_ALLOWED_CHATS` and `PEEN_TELEGRAM_ALLOWED_USERS` without replying or logging. Check the IDs and restart. See [Telegram bot](telegram.md).

## Every session fails at its first turn

A default of `docker:<profile>` on a controller with no Docker daemon in reach passes startup, and every new session then fails at its first turn. Make the daemon reachable, or change `PEEN_DEFAULT_AGENT_SPACE`.

## A rule, skill, or agent does not load

Peen checks every optional harness file on its own. If one is malformed, Peen keeps the rest, logs a warning, records a `harness.warning` event, and shows the file and the reason in the chat. Fix the reported file and send another message.

A misspelled tool name in `allowed-tools` is caught when the agent runs, and that run fails with `agent allowed-tools names unavailable tool`. [Agent tools](tools.md) lists the names.

## A session keeps spending

A session with no client attached keeps running and calling the model until something stops it. Closing the page does not stop it. Press Stop in the control surface, or call `POST /api/v1/session/cancel`. See [unattended sessions](security.md#unattended-sessions).

## Work is paused after a stop or a crash

Stop pauses unfinished work, and reconnecting leaves it paused. Ask the agent to inspect the retained task and continue it. See [recovery after a crash](agents.md#recovery-after-a-crash).

## API error codes

Error responses carry `{"code", "message"}`. These are the codes Peen adds to the [REST API](http-api.md) and to WebSocket `message.failed` frames. The [OpenAI API](openai-api.md#errors) and the [MCP control server](mcp-server.md#errors) report errors in their own formats.

| Code | Where | Meaning |
| --- | --- | --- |
| `INVALID_SESSION_ID` | REST, WebSocket upgrade | The `X-Session-ID` header, or the `sessionId` filter on `GET /ws/v1`, is malformed. |
| `SESSION_NOT_FOUND` | REST, WebSocket | The session ID is well formed, but no such session exists. Every REST operation that takes `X-Session-ID` answers this for an unknown session. |
| `NOT_FOUND` | REST | The session exists, but the record the request names inside it does not: a job, agent run, compaction, model run, snapshot, learned file, or workspace path. The message names which. |
| `CONFLICT` | REST | The request conflicts with current state: a reconfigure that names the agent space the workspace already uses, or deleting a directory that is not empty. |
| `SESSION_BUSY` | REST, WebSocket | The session already has an active turn. |
| `TURN_CANCELLED` | WebSocket | The turn ended because its session was cancelled. |
| `TURN_TIMEOUT` | WebSocket | The turn reached its overall deadline. Increase `timeoutSeconds` or `PEEN_TURN_TIMEOUT`. |
| `OUTPUT_SCHEMA_INVALID` | WebSocket | The schema cannot compile without external resources or exceeds `PEEN_MAX_OUTPUT_SCHEMA_BYTES`. |
| `USER_MESSAGE_QUEUE_FULL` | WebSocket | The active turn cannot accept another queued message. |
| `AGENT_SPACE_NOT_ALLOWED` | REST | The deployment does not offer that agent space, and the response never names the ones it does. A reconfigure of the system session also answers this, because the system session's agent space cannot change. |
| `AGENT_SPACE_CONFLICT` | REST, WebSocket | The request named an agent space other than the one its workspace's sessions share. The message names the current one. Leave the agent space out, name that one, or change it with [`POST /api/v1/session/reconfigure`](https://psyb0t.github.io/peen/api/reconfigure-session/). |
| `SYSTEM_AGENT_OFF` | REST | A system session was requested while `PEEN_SYSTEM_AGENT` is off. |
| `WORKSPACE_NOT_ALLOWED` | REST | The workspace falls outside the workspace root, or would expose Peen's own directories, or a file path leaves the workspace. See [workspaces](where-it-runs.md#workspaces-that-would-expose-peens-own-directories). |
| `WORKSPACE_NOT_FOUND` | REST | The workspace is allowed, but its directory no longer exists. |
| `AGENT_FINISHED` | WebSocket | A message went to a child agent that has already ended. |
| `AGENT_NOT_FOUND` | WebSocket | A message went to an agent run the session never had. |
| `HARNESS_CONFIGURATION_INVALID` | WebSocket | A harness safety bound stops the turn from starting. |
| `TURN_ROUND_LIMIT` | WebSocket | The turn used every round `PEEN_MAX_TOOL_ROUNDS` allows. The message names the limit. |
| `TURN_TOKEN_LIMIT` | WebSocket | The turn billed the tokens `PEEN_MAX_TURN_TOKENS` allows. The message names the limit. |

## Still stuck

Open an issue at [github.com/psyb0t/peen/issues](https://github.com/psyb0t/peen/issues) with the `peen doctor` output and the relevant `peen logs` lines. Check the lines for secrets first.
