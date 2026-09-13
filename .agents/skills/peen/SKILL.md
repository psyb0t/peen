---
name: peen
description: Configure and operate Peen, the durable coding-agent backend. Use when setting up a provider and Docker workspace, sending work over WebSocket, adding project harness rules, or inspecting and controlling durable sessions.
homepage: https://github.com/psyb0t/peen
user-invocable: true
permissions:
  filesystem:
    read:
      - "README.md"
      - "docs/**"
      - ".env.example"
    write:
      - ".env"
      - "AGENTS.md"
      - ".agents/**"
  shell:
    - "docker build *"
    - "docker run *"
    - "curl *"
metadata:
  openclaw:
    emoji: "🧰"
    requires:
      bins:
        - docker
---

# Peen

Peen is a durable coding-agent backend. It works in a mounted directory,
starts turns over WebSocket, and persists the conversation, tools, model
rounds, compactions, child-agent work, and process jobs in SQLite.

## Security & safety

Treat a Peen workspace as direct shell and filesystem access for the model.
Mount only a project the operator is willing to let it change. Never mount a
home directory, Docker socket, SSH directory, or a directory containing
credentials unless the operator explicitly wants that exposure. Tool output is
stored in the transcript and delivered to connected clients, so do not let the
agent read or print secrets.

Keep provider credentials in a gitignored `.env` or a deployment secret store.
Use a real `PEEN_API_TOKEN` and `wss://` before exposing Peen beyond a trusted
local network.

## When to use

- Starting Peen for one project in Docker.
- Adding project rules, skills, agents, events, or hooks to a workspace.
- Building a WebSocket client that starts turns and follows live events.
- Reading a durable session, cancelling a turn, or inspecting jobs, model
  calls, compactions, and child-agent runs.

## When NOT to use

- Editing Peen's own Go implementation. Use the repository development docs
  and Make targets instead.
- Treating Peen as a sandbox. The deployment container, its user, mounts, and
  network policy are the actual security boundary.

## Start Peen

From a Peen source checkout, copy `.env.example` to the gitignored `.env` and
configure one provider. `PEEN_UPSTREAMS` is raw JSON for Docker, so do not
source `.env` in Bash.

Build the image, create a state directory and the single workspace you want
the model to access, then run it:

```bash
make docker-build
mkdir -p ./data/peen ./workspace
sudo chown 10001:10001 ./data/peen ./workspace

docker run --rm \
  --env-file .env \
  -p 8080:8080 \
  -v "$(pwd)/data/peen:/data/peen" \
  -v "$(pwd)/workspace:/workspace" \
  peen run
```

`PEEN_CONFIG_DIR=/data/peen` holds SQLite, logs, and an optional trusted base
harness. `PEEN_WORKING_DIR=/workspace` is the default workspace. Both host
directories must exist and be writable by UID and GID `10001` before launch.

## Send and follow work

Generate a UUIDv4 for each new conversation. Send it as
`metadata.sessionId` in a `message.send` WebSocket event. Reuse it to continue
the conversation. A normal connection gets live events for every session, so
the client builds tabs by filtering event metadata. `?sessionId=<uuid>` is an
optional server-side outbound filter only. It does not select a session for a
new message.

Use REST for durable reads and control, never to start a turn. Supply
`X-Session-ID` for session-scoped endpoints. For example, list messages with
`GET /v1/messages` and request turn cancellation with
`POST /v1/session/cancel`. The complete frame and endpoint contracts are in
the API reference.

## Teach it the project

Put always-on project rules in `AGENTS.md`. Add named procedures under
`.agents/skills/<name>/SKILL.md`, bounded child-agent definitions under
`.agents/agents/`, external-event handlers under `.agents/events/`, and
mechanical guards in `.agents/hooks.yaml`.

Peen resolves the configuration directory first, then every filesystem layer
from root to the active workspace. A closer layer is more specific. A skill
description is present in context; the model loads the full skill only when it
chooses to use it. Hooks are different: they are mechanical and can inject,
deny, run a direct command, or emit a session notice.

## References

- [Setup, providers, WebSocket, and REST](references/setup.md)
- [Configuration](../../../docs/configuration.md)
- [Hook configuration](../../../docs/hooks.md)
- [API reference](../../../docs/http-api.md)
