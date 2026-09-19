# Getting started

Peen is a coding-agent backend that works in a real directory. It keeps the
conversation, tool calls, protocol events, child-agent work, and provider
exchanges after a client disconnects. You bring the workspace and the client.
Peen runs the agent.

This gets an agent working in one folder without handing it the rest of your
machine. Peen has no bundled browser chat, so the first client below is a
browser console. Replace it with your own app when you are ready.

## What you need

- Docker.
- A provider API key.
- An existing project directory you are comfortable letting an agent inspect
  and change.
- A browser or WebSocket client.

## 1. Configure Peen

Copy the example and edit the provider section:

```bash
cp .env.example .env
```

`PEEN_UPSTREAMS` gives each provider a local name. Models use that name, such
as `zai/glm-5.3` or `aigate/your-model-id`. Set the provider endpoint, the
named key variable, and the two model variables. The default example includes
both [AIGate](https://github.com/psyb0t/aigate) and Z.ai.

`.env` is Docker `--env-file` input. Its provider value is raw JSON, so do not
source this file from Bash. Keep it out of version control.

## 2. Start it with one workspace

Build the image, create separate configuration, state, and workspace
directories, then run the server. The controller and any Docker worker must see
each host directory at the same literal path.

```bash
make docker-build
root="$PWD"
config="$root/data/peen/config"
state="$root/data/peen/state"
workspace="$root/workspace"
mkdir -p "$config" "$state" "$workspace"

docker run --rm \
  --user "$(id -u):$(id -g)" \
  --env-file .env \
  -e PEEN_CONFIG_DIR="$config" \
  -e PEEN_STATE_DIR="$state" \
  -e PEEN_HOST_USERNAME="$(id -un)" \
  -e PEEN_HOST_HOME="$HOME" \
  -p 8080:8080 \
  -v "$config:$config" \
  -v "$state:$state" \
  -v "$workspace:$workspace" \
  -w "$workspace" \
  peen run
```

Put the project you want the agent to work on in `workspace`, or point that
variable at an existing project. `config` holds the trusted harness layer.
`state` holds SQLite, logs, and worker sockets. `workspace` is the default
allowed workspace root. The Docker processes use your UID and GID, so agent
writes keep host ownership. Peen resumes the same session on a later start with
the same state directory and workspace. Do not mount your whole home directory
because the agent has normal file and command access inside its container.

## 3. Open a workspace and send a message

Peen starts with no sessions. Open the workspace first, then send a message
routed to the session it returns. With the default empty `PEEN_API_TOKEN`,
paste this into a browser console:

```js
const workspace = "/absolute/path/to/workspace";

const opened = await fetch("http://localhost:8080/v1/sessions/open", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ workspace }),
}).then((response) => response.json());

const sessionId = opened.session.id;

const socket = new WebSocket("ws://localhost:8080/v1/ws");

socket.addEventListener("message", ({ data }) => console.log(JSON.parse(data)));
socket.addEventListener("open", () => {
  socket.send(JSON.stringify({
    id: crypto.randomUUID(),
    type: "message.send",
    data: { message: "Read the project, then tell me what you would fix first." },
    metadata: { sessionId },
    timestamp: Math.floor(Date.now() / 1000),
    triggeredBy: null,
  }));
});
```

Opening the same directory again returns the same session, so this is also how
you reattach after a restart. Keep the `sessionId`: it identifies the workspace
session for REST calls and routes every later `message.send`. Leave the socket
open to see streaming model, tool, and agent events. A `message.completed`
event means the submitted task is finished. Every WebSocket receives every
session's events. Your client makes conversation tabs by filtering
`metadata.sessionId`. Add `?sessionId=<uuid>` to the WebSocket URL only when a
client wants Peen to apply that outbound filter itself.

## 4. Put project rules beside the project

Start with an `AGENTS.md` at the workspace root. Write the things an agent
needs to know every time: how to build, where tests live, what it must not
touch, and local conventions. Add the optional `.agents` directory as the work
gets more specific:

```text
workspace/
  AGENTS.md
  .agents/
    skills/<skill-name>/SKILL.md
    agents/<agent-name>.md
    events/<event-type>.md
    hooks.yaml
```

Rules and definitions nearer to a file are more specific than ones above it.
Skills give the agent named procedures. Named agents let it split off a bounded
job. Hooks are for mechanical checks and hard stops that an ordinary prompt
should not be trusted to enforce. Read [the harness configuration guide](configuration.md#harness-layering)
and [hook configuration](hooks.md) before adding hooks.

## 5. Inspect or stop a run

WebSocket starts work. REST reads durable state and controls an active session.
It never creates a turn.

```bash
curl "http://localhost:8080/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://localhost:8080/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

The API also exposes durable protocol events, outside notices, process jobs and
their output and signal history, child-agent runs, turns, prompt and context
snapshots, compaction lineage, provider runs and rounds, and session status.
[The API reference](http-api.md) has the exact frame and response shapes.

## Before you expose it

Set `PEEN_API_TOKEN` before putting Peen on a network you do not fully trust.
Use `wss://` outside local development. Mount only the directories the agent
needs, run as a non-root user, and treat the transcript as sensitive. Tool
output and files an agent reads can end up in it.

[Deployment](deployment.md) covers source builds, mounts, networking, and
the container boundary. [Configuration](configuration.md) lists every
setting. The root [README](../README.md) is the short version.
