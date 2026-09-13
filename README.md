# peen

[![CI](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/peen/badges/coverage.svg)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/peen/badges/version.svg)](https://github.com/psyb0t/peen/releases)
[![license](https://raw.githubusercontent.com/psyb0t/peen/badges/license.svg)](LICENSE)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/peen?style=flat-square)](https://hub.docker.com/r/psyb0t/peen)

Peen puts a coding agent in a real working directory and keeps the whole job
alive after the first response. Connect a client over WebSocket, give it a
task, and it can read code, edit files, run commands, use skills, launch child
agents, and follow the rules sitting beside the project.

Every conversation, tool call, agent event, context snapshot, compaction, and
provider exchange lands in SQLite. Model records keep the request and response,
thinking, usage, retries, cost, model identity, and connection name, never the
provider credential. Reconnect after a restart and the history is still there.
Every connected client receives the live feed for every session, then renders
the conversations it wants from each event's session ID.

Peen is the backend and harness. It does not ship a browser chat UI. Bring a
browser client, terminal client, bot, or your own application.

## Contents

- [Run it](#run-it)
- [Send it a task](#send-it-a-task)
- [Provider configuration](#provider-configuration)
- [Make it understand your project](#make-it-understand-your-project)
- [See what happened](#see-what-happened)
- [Things worth knowing](#things-worth-knowing)
- [Security](#security)
- [Agent integrations](#agent-integrations)
- [Documentation](#documentation)
- [What Peen is built with](#what-peen-is-built-with)

## Run it

Run Peen in Docker first. Pick a workspace you are happy to hand to an agent.
Do not mount your whole home directory just because it is convenient.

You need Docker and a provider API key. Copy the example configuration:

```bash
cp .env.example .env
```

The example has [AIGate](https://github.com/psyb0t/aigate) and Z.ai entries.
Keep the provider you use, set its model ID, and put its key in the named
environment variable. For an OpenAI-compatible
[AIGate](https://github.com/psyb0t/aigate) setup, the important lines look like
this:

```dotenv
PEEN_UPSTREAMS=[{"name":"aigate","provider":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
PEEN_DEFAULT_MODEL=aigate/your-model-id
PEEN_COMPACTION_MODEL=aigate/your-model-id
AIGATE_TOKEN=your-token-here
PEEN_API_TOKEN=
```

`.env` is a Docker `--env-file`, so leave the JSON unquoted. For a server
outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting
it.

Build the image, create a private state directory, and mount the project the
agent will work on:

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

`./data/peen` holds the database, logs, and harness configuration.
`./workspace` is the default directory the agent sees. Both survive a
container restart.

## Send it a task

With the default empty `PEEN_API_TOKEN`, open a browser console and paste this:

```js
const sessionId = crypto.randomUUID();
const socket = new WebSocket("ws://localhost:8080/v1/ws");

socket.addEventListener("message", ({ data }) => console.log(JSON.parse(data)));
socket.addEventListener("open", () => {
  socket.send(JSON.stringify({
    id: crypto.randomUUID(),
    type: "message.send",
    data: { message: "Read the project, then tell me what you would fix first." },
    timestamp: Math.floor(Date.now() / 1000),
    metadata: { sessionId },
    triggeredBy: null,
  }));
});
```

The generated `sessionId` is the conversation ID. Save it. Native agent events
arrive while it works, then `message.completed` says that submission is done.
The socket stays open for the next task.

Use the same session ID in another `message.send` to continue that
conversation. Use a new UUID when you want a clean one. A client renders a
conversation tab by filtering received events on `metadata.sessionId`. Add
`?sessionId=<uuid>` to its WebSocket URL only when it deliberately wants the
server to send one session's events. The full protocol, including browser
authentication, failed turns, queued messages, and event fields, lives in [the
WebSocket API guide](docs/http-api.md#send-and-watch-turns-over-websocket).

## Provider configuration

Give every provider a short local name. Models are then addressed as
`provider/model`, for example `aigate/your-model-id` or `zai/glm-5.3`. Peen
asks each configured provider which models it actually offers at startup. A
misspelled or unavailable model fails early instead of burning a turn.

It supports `openai`, `anthropic`, and `zai-coding` providers. `zai-coding`
keeps Z.ai thinking state through tool rounds. A `message.send` can override
the model for that one task. Peen never guesses task difficulty or silently
switches models behind your back.

The full list of provider, context, tool, and event settings is in
[Configuration](docs/configuration.md).

## Make it understand your project

`PEEN_CONFIG_DIR` holds durable state and an optional base harness. The
workspace adds project-specific instructions:

```text
workspace/
  AGENTS.md
  .agents/
    skills/<skill-name>/SKILL.md
    agents/<agent-name>.md
    events/<event-type>.md
    hooks.yaml
```

Write ordinary project rules in `AGENTS.md`. Add a skill when the agent needs a
named procedure. Add a named agent when it should delegate a bounded job. Use
hooks when the harness itself must gate, annotate, or react to an action.

Peen resolves layers from the filesystem root down to the active workspace, so
a repository can put broad rules at the top and narrow rules beside one
component. The configuration directory can add a trusted base layer and holds
the SQLite database, logs, and hook state.

Full layering, event, and hook details: [Configuration](docs/configuration.md#harness-layering)
and [Hooks](docs/hooks.md).

## See what happened

The WebSocket is for live work. REST is for durable reads and control. It never
starts a turn.

```bash
curl "http://localhost:8080/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://localhost:8080/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

REST also lists session state, durable protocol events, outside notices,
process output, child-agent runs, compaction history, and every model request
and response. REST addresses a known session through `X-Session-ID`, so a
client keeps the UUIDs for the conversations it owns. [The API
reference](docs/http-api.md) has every request and response.

## Things worth knowing

Set `PEEN_API_TOKEN` and use `wss://` outside local development. Browser
WebSockets authenticate with subprotocols because browsers cannot attach an
`Authorization` header. The [API reference](docs/http-api.md) has the
exact handshake.

Peen writes structured logs to stdout and keeps daily audit files under
`PEEN_CONFIG_DIR/logs` by default. The active workspace is a default, not a
containment boundary. An absolute tool path can still point outside it. Long
conversations either drop old request context or replace it with a stored
summary. [Configuration](docs/configuration.md) covers all of this.

## Security

Read this before you deploy Peen anywhere it can reach something you do not
want touched.

Peen's host tools, `run_command` most of all, run with exactly the access of
the operating-system user running the process. There is no sandbox, no path
allowlist, no secret-file denylist, and no approval or permission step before
a tool runs. A file tool can read, write, or remove any path that user can
touch, including `.git/`, `.env`, and SSH keys. `run_command` executes an
arbitrary shell command immediately. This is a deliberate design choice, not
a gap: the product is a coding agent with real access, and Docker, the
container user, and the mounts you choose are the isolation boundary, not
anything inside Peen itself.

Tool calls and their results are recorded verbatim in the session transcript
and sent live over the global WebSocket feed, exactly like any other message.
If the agent reads a file containing a secret, or a command prints one to
stdout, that secret now exists in the SQLite transcript and in every connected
client unless it requested a server-side session filter. Peen does not scan for
or redact secret-shaped content in tool output. Treat the transcript and the
event stream at the same sensitivity level as the files and commands the agent
can reach.

`remove_path` has exactly one built-in restriction, and it is a guard against
a catastrophic typo, not a permission system: it refuses to remove the
filesystem root or the current message's workspace directory itself. Every
other path, including everything named above, is removable.

Run Peen as a non-root user, in a container, with only the mounts, network
access, and capabilities the deployment actually needs. The production image
does exactly this by default; see below.

## Docker deployment

The local Docker command above is the normal way to run Peen. The image has a
real shell and the tools a coding agent uses. It runs as UID and GID `10001`
under `tini`. For source builds, production mounts, networking, and container
hardening, read [Deployment](docs/deployment.md).

## Agent integrations

The `peen` agent skill teaches an agent how to configure and run Peen, send
work over WebSocket, add workspace harness layers, and inspect durable state.
It is documentation only. Installing it does not start a server, run a hook,
or change a workspace.

After the next Peen release and its matching `psyb0t/agents` marketplace entry:

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install peen@psyb0t

codex plugin marketplace add psyb0t/agents
codex plugin add peen@psyb0t

openclaw skills install @psyb0t/peen
```

## Documentation

| You want to | Read |
| --- | --- |
| Start from zero | [Getting started](docs/getting-started.md) |
| Configure providers, limits, logs, and harness layers | [Configuration](docs/configuration.md) |
| Build a WebSocket or REST client | [API reference](docs/http-api.md) |
| Add hard checks or model instructions around actions | [Hook configuration](docs/hooks.md) |
| Run it outside a local Docker command | [Deployment](docs/deployment.md) |

## What Peen is built with

- [Elelem](https://github.com/psyb0t/elelem) talks to model providers, and
  [Essessey](https://github.com/psyb0t/essessey) rebuilds streamed content,
  tool calls, and thinking into durable conversation state.
- [Aichteeteapee](https://github.com/psyb0t/aichteeteapee) provides the HTTP
  server, REST error envelope, and WebSocket hub.
- [Servicepack](https://github.com/psyb0t/servicepack) owns process and service
  lifecycle plumbing. Its framework docs live in that repository. This
  repository documents Peen.
- [Gonfiguration](https://github.com/psyb0t/gonfiguration),
  [common-go](https://github.com/psyb0t/common-go),
  [commander](https://github.com/psyb0t/commander),
  [ctxerrors](https://github.com/psyb0t/ctxerrors),
  [ctxscope](https://github.com/psyb0t/ctxscope),
  [slogging](https://github.com/psyb0t/slogging), and
  [goenv](https://github.com/psyb0t/goenv) handle configuration, errors,
  common utilities, process jobs, scoped logs, log setup, and runtime
  environment detection.
- [GORM](https://github.com/go-gorm/gorm) and
  [SQLite](https://sqlite.org/) hold durable state.
- [Prometheus' Go client](https://github.com/prometheus/client_golang),
  [Cobra](https://github.com/spf13/cobra),
  [kin-openapi](https://github.com/getkin/kin-openapi), and
  [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) provide metrics,
  the command line, OpenAPI validation, and generated API clients.
