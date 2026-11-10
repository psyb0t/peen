---
title: Troubleshooting
description: Fix Peen when it refuses to start, a port or provider key is wrong, a harness file fails to load, or the API answers with an error code.
nav_order: 16
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

## The controller does not answer

`peen doctor` reports `the controller is not running` until you run `peen start`. When it reports that `/healthz` does not answer, read `peen logs`, because the error is in there.

When `GET /api/v1/models` answers with an error, check `PEEN_API_TOKEN` and the provider settings with `peen config edit`.

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

Error responses carry `{"code", "message"}`. These are the codes Peen adds to the [REST API](http-api.md).

| Code | Meaning |
| --- | --- |
| `INVALID_SESSION_ID` | The `X-Session-ID` header is malformed. |
| `SESSION_NOT_FOUND` | The session ID is well formed, but no such session exists. |
| `SESSION_BUSY` | The session already has an active turn. |
| `TURN_CANCELLED` | The turn ended because its session was cancelled. |
| `USER_MESSAGE_QUEUE_FULL` | The active turn cannot accept another queued message. |
| `AGENT_SPACE_NOT_ALLOWED` | The deployment does not offer that agent space. The response never names the ones it does. |
| `SYSTEM_AGENT_OFF` | A system session was requested while `PEEN_SYSTEM_AGENT` is off. |
| `WORKSPACE_NOT_ALLOWED` | The workspace falls outside the workspace root, or would expose Peen's own directories. See [workspaces](where-it-runs.md#workspaces-that-would-expose-peens-own-directories). |
| `WORKSPACE_NOT_FOUND` | The workspace is allowed, but its directory no longer exists. |
| `AGENT_FINISHED` | A message went to a child agent that has already ended. |
| `AGENT_NOT_FOUND` | A message went to an agent run the session never had. |
| `HARNESS_CONFIGURATION_INVALID` | A harness safety bound stops the turn from starting. |
| `TURN_ROUND_LIMIT` | The turn used every round `PEEN_MAX_TOOL_ROUNDS` allows. The message names the limit. |
| `TURN_TOKEN_LIMIT` | The turn billed the tokens `PEEN_MAX_TURN_TOKENS` allows. The message names the limit. |

## Still stuck

Open an issue at [github.com/psyb0t/peen/issues](https://github.com/psyb0t/peen/issues) with the `peen doctor` output and the relevant `peen logs` lines. Check the lines for secrets first.
