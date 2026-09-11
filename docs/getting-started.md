# Getting started

Peen is a coding-agent backend that works in a real directory. It keeps the
conversation, tool calls, events, and child-agent work after a client
disconnects. You bring the workspace and the client. Peen runs the agent.

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
both AIGate and Z.ai.

`.env` is Docker `--env-file` input. Its provider value is raw JSON, so do not
source this file from Bash. Keep it out of version control.

## 2. Start it with one workspace

Build the image, create the durable state and workspace directories, then run
the server:

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

Put the project you want the agent to work on in `./workspace`, or mount that
project there instead. `./data/peen` holds the SQLite database, logs, hooks,
and other durable harness state. Do not mount your home directory because the
agent has normal file and command access inside its container.

## 3. Send a message

With the default empty `PEEN_API_TOKEN`, paste this into a browser console:

```js
const sessionId = crypto.randomUUID();
const socket = new WebSocket(
  `ws://localhost:8080/v1/ws?sessionId=${sessionId}`,
);

socket.addEventListener("message", ({ data }) => console.log(JSON.parse(data)));
socket.addEventListener("open", () => {
  socket.send(JSON.stringify({
    type: "message.send",
    data: { message: "Read the project, then tell me what you would fix first." },
  }));
});
```

Keep the `sessionId`. It is the conversation ID. Leave the socket open to see
streaming model, tool, and agent events. A `message.completed` event means the
submitted task is finished. Connect another client with the same ID when you
want both clients to watch or contribute to the same conversation.

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

The API also exposes queued events, process jobs, child-agent runs, and session
status. [The API reference](http-api.md) has the exact frame and response
shapes.

## Before you expose it

Set `PEEN_API_TOKEN` before putting Peen on a network you do not fully trust.
Use `wss://` outside local development. Mount only the directories the agent
needs, run as a non-root user, and treat the transcript as sensitive. Tool
output and files an agent reads can end up in it.

[Deployment](deployment.md) covers source builds, mounts, networking, and
the container boundary. [Configuration](configuration.md) lists every
setting. The root [README](../README.md) is the short version.
