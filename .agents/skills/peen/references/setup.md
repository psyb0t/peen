# Set up and use Peen

## Configure a provider

Copy `.env.example` to `.env`. Keep only the upstreams you intend to use, set
`PEEN_DEFAULT_MODEL`, and set the matching credential variable. Provider
credentials belong in the named environment variable, never inside
`PEEN_UPSTREAMS`.

```dotenv
PEEN_UPSTREAMS=[{"name":"aigate","provider":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
PEEN_DEFAULT_MODEL=aigate/your-model-id
PEEN_COMPACTION_MODEL=aigate/your-model-id
AIGATE_TOKEN=your-token-here
PEEN_API_TOKEN=
```

`openai`, `anthropic`, and `zai-coding` are supported upstream types. Peen
checks that the configured model is available when it starts. Set
`PEEN_API_TOKEN` before running anywhere other than an intentionally
unauthenticated local environment.

## Start the Docker server

The server needs a private state directory and one deliberate workspace mount.
The state directory stores the SQLite transcript, logs, and configuration
harness. It and the workspace must exist before the non-root container starts.

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

Do not publish the metrics listener. It defaults to the container's loopback
interface. Do not add the Docker socket to this container. For stronger
isolation, restrict the network and use a read-only root filesystem with only
the required writable mounts.

## Send a turn

WebSocket is the only turn-creation surface. REST is read and control only.

```js
const sessionId = crypto.randomUUID();
const socket = new WebSocket("ws://localhost:8080/v1/ws");

socket.addEventListener("message", ({ data }) => console.log(JSON.parse(data)));
socket.addEventListener("open", () => {
  socket.send(JSON.stringify({
    id: crypto.randomUUID(),
    type: "message.send",
    data: { message: "Read the project, then explain the first safe change." },
    timestamp: Math.floor(Date.now() / 1000),
    metadata: { sessionId },
    triggeredBy: null,
  }));
});
```

Keep `sessionId`. Sending another `message.send` with that ID continues the
session. Each accepted event carries `metadata.sessionId`, so clients sharing
a global socket can render only the session tab they need. A
`message.completed` event means that submitted message is done. It does not
close the socket.

With `PEEN_API_TOKEN` set, browser clients authenticate through the
`peen.v1` and `peen.bearer.<base64url-token>` WebSocket subprotocols. Non-browser
clients can use the normal `Authorization: Bearer <token>` upgrade header.
Never put a token in the URL.

## Inspect and control durable work

REST reads state stored in SQLite. Use `X-Session-ID` on session endpoints.

```bash
curl "http://localhost:8080/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://localhost:8080/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

The API also lists protocol events, context and prompt snapshots, compaction
lineage, model runs and calls, process jobs and output, child-agent runs, and
external notices. Use `wss://` and HTTPS when Peen is not local.

## Add a harness layer

```text
workspace/
  AGENTS.md
  .agents/
    skills/<skill-name>/SKILL.md
    agents/<agent-name>.md
    events/<event-type>.md
    hooks.yaml
```

`AGENTS.md` gives the model standing project instructions. Skills are
on-demand procedures. Named agents handle bounded delegated work. Event
handlers tell Peen how to process an external notice. Hooks enforce mechanical
behavior around lifecycle and tool events. Workspace hook commands require
`PEEN_ENABLE_WORKSPACE_HOOKS=true`, and should only be enabled for trusted
workspaces.
