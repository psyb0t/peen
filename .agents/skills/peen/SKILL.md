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
- Assuming a session is sandboxed. The execution profile the operator gave it
  decides that, and the default `native` profile runs tools with the
  controller's own access.

## Start Peen

Create separate configuration, state, and workspace root directories, then
download `.env.example` as `.env` and configure one provider. Delete the
provider entries you do not use, together with their key variables.
`PEEN_UPSTREAMS` is raw JSON for Docker, so do not source `.env` in Bash.

```bash
root="$HOME/.local/share/peen"
config="$root/config"
state="$root/state"
workspace_root="$HOME/work"
mkdir -p "$config" "$state" "$workspace_root/my-app"
curl -fsSLo "$root/.env" https://raw.githubusercontent.com/psyb0t/peen/main/.env.example

docker run --rm \
  --user "$(id -u):$(id -g)" \
  --env-file "$root/.env" \
  -e PEEN_CONFIG_DIR="$config" \
  -e PEEN_STATE_DIR="$state" \
  -e PEEN_HOST_USERNAME="$(id -un)" \
  -e PEEN_HOST_HOME="$HOME" \
  -p 8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace_root:$workspace_root" \
  -w "$workspace_root" \
  psyb0t/peen:latest
```

`PEEN_CONFIG_DIR` holds the optional trusted base harness, and every worker
reads it read-only. `PEEN_STATE_DIR` holds SQLite, logs, and worker sockets,
and no worker receives it. Peen refuses to start when one sits inside the
other. Each directory is mounted at its literal host path. The working
directory `-w "$workspace_root"` is the workspace root, and
`PEEN_WORKSPACE_ROOT` sets a different one. A client may open the root or any
directory inside it as a workspace. The container runs as the operator's UID
and GID, so agent writes keep host ownership.

## Send and follow work

Peen starts with no sessions. Open a workspace with
`POST /v1/sessions/open` and a body of `{"workspace":"<workspace_root>/my-app"}`,
using the absolute host path. It returns
the durable session, creating it the first time that directory is opened and
resuming it afterwards. Never invent a session ID: an unknown one is refused
and starts no turn.

Send the returned ID as `metadata.sessionId` in a `message.send` WebSocket
event, and reuse it to continue the conversation. A normal connection gets live
events for every session, so the client builds tabs by filtering event
metadata. `?sessionId=<uuid>` is an optional server-side outbound filter only.
It does not route a new message.

`GET /v1/sessions` lists what exists and needs no session header.

Use REST for durable reads and control, never to start a turn. Supply
`X-Session-ID` for session-scoped endpoints. For example, list messages with
`GET /v1/messages` and request turn cancellation with
`POST /v1/session/cancel`. The complete frame and endpoint contracts are in
the API reference.

## Teach it the project

Put always-on project instructions in `AGENTS.md` and topic rules in
`.agents/rules/<name>.md`. Add named procedures under
`.agents/skills/<name>/SKILL.md`, bounded child-agent definitions under
`.agents/agents/`, external-event handlers under `.agents/events/`, and
mechanical guards in `.agents/hooks.yaml`.

Peen resolves the configuration directory first, then every filesystem layer
from root to the active workspace, at the start of every turn. A layer closer
to the workspace is more specific. Below the workspace, only `AGENTS.md` files
are read, each scoped to its own directory, and the closest one wins. A skill
description is present in context; the model loads the full skill only when it
chooses to use it. Hooks are different: they are mechanical and can inject,
deny, run a direct command, or emit a session notice.

## References

- [Setup, providers, WebSocket, and REST](references/setup.md)
- [Configuration](../../../docs/configuration.md)
- [The harness](../../../docs/harness.md)
- [AGENTS.md and rules](../../../docs/rules.md)
- [Skills](../../../docs/skills.md)
- [Named agents](../../../docs/agents.md)
- [Session events](../../../docs/events.md)
- [Hooks](../../../docs/hooks.md)
- [API reference](../../../docs/http-api.md)
