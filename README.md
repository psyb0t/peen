# peen

[![CI](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/peen/badges/coverage.svg)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/peen/badges/version.svg)](https://github.com/psyb0t/peen/releases)
[![license](https://raw.githubusercontent.com/psyb0t/peen/badges/license.svg)](LICENSE)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/peen?style=flat-square)](https://hub.docker.com/r/psyb0t/peen)

_peen goes in vageen_

Peen puts a coding agent in a real working directory and keeps the whole job
alive after the first response. Connect a client over WebSocket, give it a
task, and it can read code, edit files, run commands, use skills, launch child
agents, and follow the `AGENTS.md` and rules sitting beside the project.

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

Peen ships its own browser control surface at the controller's root URL. It is one client among many, so a terminal client, bot, or your own application can watch the same live work and use the same durable records.

## Contents

- [Install](#install)
- [Run it](#run-it)
- [Use the control surface](#use-the-control-surface)
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

The normal install is the published Docker image:

```bash
docker pull psyb0t/peen:latest
```

The image starts Peen's control plane by default. You need Docker and a provider
API key. The next section creates the configuration and starts it.

If you want a native binary instead, use the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

Read [install.sh](install.sh) before piping it into a shell. [Deployment](docs/deployment.md) covers native installs, source builds, and running Peen under a process manager.

## Run it

Run Peen in Docker first. Pick a workspace root you are happy to hand to an agent: a directory that holds the projects you want it to work on. Do not mount your whole home directory just because it is convenient.

Two words matter here. The workspace root is the one directory the controller may hand out. A workspace is a project directory inside it that you open as a chat, and each workspace gets its own agent session.

You need Docker and a provider API key. Pick three host directories: one for trusted configuration, one for Peen's database and logs, and the workspace root. Download the example configuration beside them:

```bash
root="$HOME/.local/share/peen"
config="$root/config"
state="$root/state"
workspace_root="$HOME/work"
mkdir -p "$config" "$state" "$workspace_root/my-app"
curl -fsSLo "$root/.env" https://raw.githubusercontent.com/psyb0t/peen/main/.env.example
${EDITOR:-vi} "$root/.env"
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

Set the provider URL, model IDs, and the named API-key environment variable in `$root/.env`. `PEEN_CONFIG_DIR` and `PEEN_STATE_DIR` are separate on purpose. Workers get the first one read-only and never get the second. Peen refuses to start if one sits inside the other.

The file is a Docker `--env-file`, so leave the JSON unquoted. For a server outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting it.

Mount each directory at its literal host path. Literal paths matter when the controller starts Docker workers. Docker resolves worker mounts on the host, not inside the controller container.

```bash
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

The control process and workers run as your UID and GID, so files the agent
creates stay yours. `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME` let a Docker
worker recreate that account inside its own image. `config` holds the trusted
harness layer and workers receive it read-only. `state` holds the database,
audit logs, and worker sockets and workers never receive it. `workspace_root` is the process working directory, which makes it the workspace root. Set `PEEN_WORKSPACE_ROOT` to use a different one. Peen starts with no sessions and opens one when a client names a workspace inside the root. It resumes the same session when it restarts with the same state directory and the same workspace. All three mounts survive a container restart.

## Use the control surface

Open `http://localhost:8080` after Peen starts. If `PEEN_API_TOKEN` is set, enter it in the connection form. The browser keeps it in page memory only. It does not put the token in a URL or browser storage.

The sidebar shows the workspace root. Type a workspace inside it, such as `$HOME/work/my-app`, and open the chat. Peen returns its existing session when that directory was opened before, otherwise it creates the one durable session for that workspace. Open `$HOME/work/other-app` for a second project with its own agent. The composer shows the model and reasoning level the next turn will use, starting from the session's model, and you can change either for that turn before you send the work. Replies stream in as the model writes them, rendered as Markdown, with each tool call shown as one card next to its result.

The socket receives the live feed for every session. The control surface keeps that global feed intact, but shows the selected session's events and durable records in its own tab. It also exposes cancellation, execution-profile changes, transcript messages, model runs, child agents, compactions, workers, jobs, and notices.

The browser control surface is embedded in the Peen binary. There is no frontend service or browser token persistence to operate. Build another client against the [WebSocket and REST API guide](docs/http-api.md) when you need a different interface or automation.

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
the model and its reasoning level for that one task. Peen never guesses task difficulty or silently
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
  some/subdirectory/AGENTS.md
  .claude/
    rules/<rule-name>.md
    skills/<skill-name>/SKILL.md
  .agents/
    rules/<rule-name>.md
    skills/<skill-name>/SKILL.md
    agents/<agent-name>.md
    events/<event-type>.md
    hooks.yaml
```

Peen reads these files from the configuration directory and from every directory between `/` and the workspace, at the start of every turn. Edit one and the next message sees it. A broken file is skipped with a warning in the chat, and everything else keeps working. [The harness guide](docs/harness.md) has the layer order and limits.

**AGENTS.md.** The project's instructions file, in the [agents.md](https://agents.md) format other coding agents read too: build commands, conventions, and things the agent must not touch. It goes into the system prompt on every turn. Files from every layer add up, so `~/work/AGENTS.md` covers all your projects and `~/work/my-app/AGENTS.md` adds to it. An `AGENTS.md` below the workspace covers its own directory, and the closest one wins. [AGENTS.md and rules](docs/rules.md)

**Rules.** Topic files in `.agents/rules/` or `.claude/rules/`, one subject each, such as `testing.md` or `go.md`. They also go into the system prompt on every turn and add up across layers. [AGENTS.md and rules](docs/rules.md#rules)

**Skills.** A skill is a directory with a `SKILL.md`: YAML frontmatter with a `name` and `description`, then the procedure. The model sees every skill's name and description each turn and loads the full text with `use_skill` when a task fits. Write `:skill-name` in a message to force one. [Skills](docs/skills.md)

**Named agents.** A file in `.agents/agents/` defines a child agent with its own instructions and an optional `allowed-tools` list. The main agent hands it a job with `launch_agent` and gets its final answer back. Every child run is stored and inspectable. [Named agents](docs/agents.md)

**Session events.** When a background command finishes, a child agent ends, a hook reports something, or an outside system posts to `/v1/session/notices`, Peen records an event and hands it to the model at the next turn or tool boundary. A handler in `.agents/events/<type>.md` can make an event start a turn on an idle session. [Session events](docs/events.md)

**Hooks.** `.agents/hooks.yaml` runs actions at 38 points in a turn: before and after each message, turn, compaction, and tool call. An action can block the operation, run a command, add context for the model, or publish an event. [Hooks](docs/hooks.md)

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

The Docker command above is the normal way to run Peen. The image has a real shell and the tools a coding agent uses. The command runs it as your UID and GID so controller and worker changes keep host ownership. For production mounts, networking, and container hardening, read [Deployment](docs/deployment.md).

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
| Configure providers, limits, and logs | [Configuration](docs/configuration.md) |
| Understand how Peen reads project files | [The harness](docs/harness.md) |
| Give the agent project instructions and rules | [AGENTS.md and rules](docs/rules.md) |
| Write named procedures the agent loads on demand | [Skills](docs/skills.md) |
| Define child agents for contained jobs | [Named agents](docs/agents.md) |
| React to background jobs, child agents, and outside systems | [Session events](docs/events.md) |
| Add hard checks or model instructions around actions | [Hooks](docs/hooks.md) |
| Build a WebSocket or REST client | [API reference](docs/http-api.md) |
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
