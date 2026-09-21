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

One host runs one control plane. It owns SQLite, the API, and the event feed,
and it starts a separate worker process per session to run that session's turns.
Which environment a worker gets, a child process or its own container, is an
operator decision. See [Architecture](docs/architecture.md).

Peen is the backend and harness. It does not ship a browser chat UI. Bring a
browser client, terminal client, bot, or your own application.

## Contents

- [Install](#install)
- [Run it](#run-it)
- [Send it a task](#send-it-a-task)
- [Provider configuration](#provider-configuration)
- [Make it understand your project](#make-it-understand-your-project)
- [See what happened](#see-what-happened)
- [Drive it from the command line](#drive-it-from-the-command-line)
- [Things worth knowing](#things-worth-knowing)
- [Security](#security)
- [Docker deployment](#docker-deployment)
- [Agent integrations](#agent-integrations)
- [Documentation](#documentation)
- [What Peen is built with](#what-peen-is-built-with)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

That clones Peen into a temporary directory, builds it, installs the binary to
`~/bin`, and deletes the clone. Set `PREFIX` for somewhere else and `REF` for a
tag or branch:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh |
  PREFIX=/usr/local/bin REF=v0.10.1 bash
```

Piping a script from the internet into a shell is worth a look first. Read it at
[install.sh](install.sh), or do the same thing by hand:

```bash
git clone https://github.com/psyb0t/peen.git
cd peen
make install
```

Either way you need Docker. The build runs in a pinned Go image, so no local Go
toolchain is involved. `make install` puts the binary in `~/bin` unless you pass
`PREFIX`. [Deployment](docs/deployment.md) covers the other routes, including
`go install` and building the image yourself.

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
PEEN_CONFIG_DIR=/absolute/path/to/peen/config
PEEN_STATE_DIR=/absolute/path/to/peen/state
PEEN_UPSTREAMS=[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
PEEN_DEFAULT_MODEL=aigate/your-model-id
PEEN_COMPACTION_MODEL=aigate/your-model-id
AIGATE_TOKEN=your-token-here
PEEN_API_TOKEN=
```

`PEEN_CONFIG_DIR` and `PEEN_STATE_DIR` are separate on purpose. Workers get the
first one read-only and never get the second. Peen refuses to start if one sits
inside the other.

`.env` is a Docker `--env-file`, so leave the JSON unquoted. For a server
outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting
it.

Build the image, create separate configuration, state, and workspace
directories, then mount each at its literal host path. Literal paths matter
when the controller starts Docker workers. Docker resolves worker mounts on the
host, not inside the controller container.

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

The control process and workers run as your UID and GID, so files the agent
creates stay yours. `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` let a Docker
worker recreate that account inside its own image. `config` holds the trusted
harness layer and workers receive it read-only. `state` holds the database,
audit logs, and worker sockets and workers never receive it. `workspace` is the
process working directory and agent workspace. Peen starts with no sessions and
opens one when a client names that directory. It resumes the same session when
it restarts with the same state directory and workspace. All three mounts
survive a container restart.

## Send it a task

Peen starts with no sessions, so open the workspace first, then route a message
to the session it returns. With the default empty `PEEN_API_TOKEN`, open a
browser console and paste this:

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
you reattach after a restart. Save the `sessionId` for REST reads and controls.
Native agent events arrive while it works, then `message.completed` says that
submission is done. The socket stays open for the next task, which names the
same session.

Every connected client receives every session's live events. A client renders
tabs by filtering received events on `metadata.sessionId`. Add
`?sessionId=<uuid>` to its WebSocket URL only when it deliberately wants the
server to send one session's events. The full protocol, including browser
authentication, failed turns, queued messages, and event fields, lives in [the
WebSocket API guide](docs/http-api.md#send-and-watch-turns-over-websocket).

## Provider configuration

Give every provider a short local name. Models are then addressed as
`provider/model`, for example `aigate/your-model-id` or `zai/glm-5.3`. Peen
asks each configured provider which models it actually offers at startup. A
misspelled or unavailable model fails early instead of burning a turn.

Each upstream also declares a `type`, which is the wire protocol it speaks
rather than the vendor behind it. An OpenAI-compatible gateway is
`type: "openai"` whoever runs it. The supported types are `openai`,
`anthropic`, and `zai-coding`. `zai-coding` keeps Z.ai thinking state through
tool rounds. A `message.send` can override
the model for that one task. Peen never guesses task difficulty or silently
switches models behind your back.

The full list of provider, context, tool, and event settings is in
[Configuration](docs/configuration.md).

## Make it understand your project

`PEEN_CONFIG_DIR` holds an optional trusted base harness. `PEEN_STATE_DIR`
holds durable controller state and is never mounted into a worker. The workspace
adds project-specific instructions:

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
component. The configuration directory can add a trusted base layer.

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

## Drive it from the command line

The same binary is also a local control client. Each command talks to a running
controller over the control API. None of them opens the database or starts a
second supervisor: when nothing is listening they start `peen run` and wait for
it to answer.

```bash
peen control status
peen session open /srv/work/project
peen session list
peen session attach <session-id>
peen session stop <session-id>
```

`peen control status` reports reachability without starting anything, so it
tells "not running" apart from "running". `peen session open` prints the
session ID, its workspace, and whether the call created the session or resumed
one. `peen session attach` streams that session's live events and starts no
turn. `peen session stop` cancels the session's active turn and says when there
was nothing running. The commands read the same `PEEN_` configuration the
controller does, so a command and its controller cannot disagree about the
endpoint.

A native deployment currently reaches the controller over the configured
loopback HTTP endpoint. Unix-domain-socket discovery is not implemented: the
vendored HTTP server creates TCP listeners only and exposes no way to supply
one, so it needs an upstream capability first.

## Things worth knowing

Set `PEEN_API_TOKEN` and use `wss://` outside local development. Browser
WebSockets authenticate with subprotocols because browsers cannot attach an
`Authorization` header. The [API reference](docs/http-api.md) has the
exact handshake.

Peen writes structured logs to stdout and keeps daily audit files under
`PEEN_STATE_DIR/logs` by default. The active workspace is a default, not a
containment boundary. An absolute tool path can still point outside it. Long
conversations either drop old request context or replace it with a stored
summary. [Configuration](docs/configuration.md) covers all of this.

## Security

Read this before you deploy Peen anywhere it can reach something you do not
want touched.

A session's tools run in a worker process under the execution profile the
operator picked for that session. On the default `native` profile that worker is
a child of the controller, so `run_command` and the file tools have exactly the
access of the operating-system user running Peen. There is no path allowlist, no
secret-file denylist, and no approval or permission step before a tool runs. A
file tool can read, write, or remove any path that user can touch, including
`.git/`, `.env`, and SSH keys. `run_command` executes an arbitrary shell command
immediately. That is deliberate: the product is a coding agent with real access.

A `docker` profile is the isolation boundary. It runs the worker in its own
container with only the mounts, network, and capabilities the operator defined.
A client picks a profile by name and never sends an image, mount, network
setting, or capability, so the blast radius is an operator decision. If the
controller cannot reach a Docker socket, a session on a Docker profile is
refused rather than run on the host. See
[Configuration](docs/configuration.md#execution-profiles).

A worker container receives the workspace, `PEEN_CONFIG_DIR` read-only, its own
session socket directory, and only the runtime configuration it needs, including
the named provider credential for its model calls. It does not receive
`PEEN_STATE_DIR`, the control API token, or the controller Docker socket. The
provider credential is therefore available to the agent process. Treat it like
any other secret exposed inside an agent workspace. Peen runs the image published
alongside the running build unless `PEEN_WORKER_IMAGE` or the profile names
another. The container starts as root and the Peen entrypoint drops it back to
your host account, so an image without that entrypoint fails when the worker
runs.

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
filesystem root or the session's own workspace directory. Every
other path, including everything named above, is removable.

Run Peen as a non-root user, in a container, with only the mounts, network
access, and capabilities the deployment actually needs. The production image
does exactly this by default; see below.

## Docker deployment

The local Docker command above is the normal way to run Peen. The image has a
real shell and the tools a coding agent uses. Its image default is a non-root
account, and the documented command deliberately overrides that with your UID
and GID so controller and worker changes keep host ownership. For source builds,
production mounts, networking, and container hardening, read
[Deployment](docs/deployment.md).

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
