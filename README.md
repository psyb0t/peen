# peen

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/peen.svg)](https://pkg.go.dev/github.com/psyb0t/peen)
[![CI](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/peen/badges/coverage.svg)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/peen/badges/version.svg)](https://github.com/psyb0t/peen/releases)
[![license](https://raw.githubusercontent.com/psyb0t/peen/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/peen/badges/importers.svg)](https://github.com/psyb0t/peen/blob/badges/importers.md)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/peen?style=flat-square)](https://hub.docker.com/r/psyb0t/peen)

_peen goes in vageen_

Peen is a coding agent built as a server. One control server opens a session for each workspace directory and runs Peen's agent for that session in its own worker, either a process beside the controller or a separate Docker container. Clients send work and watch it live over WebSocket, and the agent keeps working when they disconnect. It can read code, edit files, run commands, use skills, launch child agents, and follow the `AGENTS.md` and rules sitting beside the project.

Every conversation, tool call, agent event, context snapshot, compaction, and provider exchange lands in SQLite. Model records keep the request and response, thinking, usage, retries, cost, model identity, and connection name, never the provider credential. Reconnect after a restart and the history is still there. A global WebSocket receives every session's live feed; a session-filtered socket receives only that session.

Root and nested agent conversations run through SQLite-backed go-workflows. After a controller or worker crash, Peen restores saved context and reuses completed activity results. Stop pauses unfinished work, and reconnecting leaves it paused. Ask an agent to inspect and explicitly continue the retained task; uncertain tool effects need inspection before retry. See [recovery and continuation](docs/agents.md#recovery-after-a-crash).

One host runs one control plane. It owns SQLite, the API, and the event feed, and the operator chooses whether workers run as child processes or containers. See [Where it runs](docs/where-it-runs.md) and [Architecture](docs/architecture.md).

Peen ships its own browser control surface at the controller's root URL. It is one client among many, so a terminal client, a bot, or your own application can watch the same live work and use the same stored records.

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

You need Linux, a model provider API key, and, for the default setup, Docker that your user can run.

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

Read [install.sh](install.sh) before piping it into a shell. It installs the `peen` command in `~/.local/bin`, pins the newest release image, and writes your configuration to `~/.config/peen/.env`.

Two settings decide where things run. The control space is where Peen itself runs, and the agent space is where each workspace's agent runs. By default both are `docker`, so Peen runs in a container and gives each workspace's agent its own container. `--control-space host` runs Peen as a binary on your machine instead, and `--agent-space control` runs the agent next to Peen. Host plus control needs no Docker at all. [Where it runs](docs/where-it-runs.md) says what the agent can touch in each of the four combinations, and [Deployment](docs/deployment.md#install) lists every flag, the `peen` commands, and the manual routes.

## Run it

Two words matter here. The workspace root is the one directory Peen may open workspaces in, your home directory unless you passed `--workspace-root`. A workspace is a project directory inside it that you open as a chat, and each workspace gets its own agent session.

Add your model provider first. Peen refuses to start until the provider that serves your default model answers and every key the file names is set.

```bash
peen config edit
```

The file has [AIGate](https://github.com/psyb0t/aigate) and Z.ai entries. Keep the provider you use, set both `PEEN_DEFAULT_MODEL` and `PEEN_COMPACTION_MODEL` to one of its models, and put its key in the named environment variable. Delete the other entry together with its key variable, because Peen contacts every listed provider at startup and refuses to start when a named key variable is missing or empty. For an OpenAI-compatible [AIGate](https://github.com/psyb0t/aigate) setup, the important lines look like this:

```dotenv
PEEN_UPSTREAMS=[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
PEEN_DEFAULT_MODEL=aigate/your-model-id
PEEN_COMPACTION_MODEL=aigate/your-model-id
AIGATE_TOKEN=your-token-here
PEEN_API_TOKEN=
```

The file is a Docker `--env-file`, so leave the JSON unquoted. The installer already set the directories, the image, and the agent space. Change the image with `peen upgrade` and the control space with `peen setup`, and see [where it runs](docs/where-it-runs.md) for agent spaces and Docker profiles. For a server outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting it. `peen doctor` checks the result.

Then open a project:

```bash
cd ~/work/my-app
peen
```

`peen` starts Peen when it is not running, opens the session for the current directory, and prints its link, `http://127.0.0.1:8080/?session=<id>`, opening it in your browser when it can. Run it again in the same directory and you get the same session back, with its history. Open `~/work/other-app` for a second project with its own agent. `peen status`, `peen logs`, `peen stop`, and `peen upgrade` manage the controller, and [Deployment](docs/deployment.md#the-peen-command) lists the rest.

Peen keeps two directories under `~/.config/peen`. `config` holds the trusted harness layer, which Docker workers get read-only. `state` holds the session and workflow databases, audit logs, and worker sockets, which no Docker worker receives. Peen refuses a workspace that contains either directory, or sits inside the state directory, so with the default workspace root you open project directories, not your home directory itself. Everything survives a restart.

## Use the control surface

Open the link `peen` printed, or `http://127.0.0.1:8080`, after Peen starts. If `PEEN_API_TOKEN` is set, enter it in the connection form. The browser keeps it in page memory only. It does not put the token in a URL or browser storage.

The sidebar shows the workspace root. Type a workspace inside it, such as `$HOME/work/my-app`, and open the chat. Peen returns its existing session when that directory was opened before, and otherwise creates the one session for that workspace. Open `$HOME/work/other-app` for a second project with its own agent. The composer shows the model and reasoning level the next turn will use, starting from the session's model, and you can change either for that turn before you send the work. The input box spans the full width. Enter sends, and Shift+Enter starts a new line. Your messages show as bubbles on the right. The agent's replies take the full width with no bubble, stream in as the model writes them, and render as Markdown, with each tool call shown as one card next to its result. A message you send while a turn is running shows "Queued. Lands after the current step." until the agent receives it.

The browser keeps one global event socket. It reconnects automatically when the connection drops and catches up on whatever it missed from the session feed. Selecting a chat only changes what the control surface renders from that socket; it no longer opens a second, session-filtered connection, and losing or closing the socket no longer pauses any session's work. The Stop control still pauses the selected session, reconnecting does not resume work Stop already paused, and stopping one child permanently cancels its unfinished subtree.

The global socket receives the live feed for every session. The control surface keeps that global feed intact, but shows the selected session's events and stored records in its own tab. It also offers cancellation, agent space changes, transcript messages, model runs, child agents, compactions, workers, jobs, and notices.

The browser control surface is embedded in the Peen binary. There is no frontend service to run, and no token stored in the browser. Build another client against the [WebSocket and REST API guide](docs/http-api.md) when you need a different interface or automation.

## Provider configuration

Give every provider a short local name. Models are then addressed as `provider/model`, for example `aigate/your-model-id` or `zai/glm-5.3`. Peen asks each configured provider which models it offers at startup, so a misspelled or unavailable model fails before it costs a turn. [Configuration](docs/configuration.md#providers) describes every field of a provider entry.

Each upstream also declares a `type`, which is the wire protocol it speaks rather than the vendor behind it. An OpenAI-compatible gateway is `type: "openai"` whoever runs it. The supported types are `openai`, `anthropic`, and `zai-coding`. `zai-coding` keeps Z.ai thinking state through tool rounds. A message sent over the WebSocket (`message.send`, see the [API reference](docs/http-api.md#send-and-watch-turns-over-websocket)) can override the model and its reasoning level for that one task. Peen never picks a model by guessing how hard a task is, and never switches models on its own.

The full list of provider, context, tool, and event settings is in [Configuration](docs/configuration.md).

## Make it understand your project

`PEEN_CONFIG_DIR` holds an optional trusted base harness. `PEEN_STATE_DIR` holds the controller's stored state and is never mounted into a Docker worker. The workspace adds project-specific instructions:

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

Peen reads these files from the configuration directory and from every directory between `/` and the agent's current directory, at the start of every turn and on each `changeDirectory` call. Enter a subdirectory to load all its harness pieces. Edit one and the next message or hop sees it. A broken file is skipped with a warning in the chat, and everything else keeps working. [The harness guide](docs/harness.md) has the layer order and limits.

**AGENTS.md.** The project's instructions file, in the [agents.md](https://agents.md) format other coding agents read too. It holds build commands, conventions, and things the agent must not touch, and it goes into the system prompt on every turn. Files from every layer add up, so `~/work/AGENTS.md` covers all your projects and `~/work/my-app/AGENTS.md` adds to it. A directory's `AGENTS.md` loads when the agent enters it, and the closest one wins. [AGENTS.md and rules](docs/rules.md)

**Rules.** Topic files in `.agents/rules/` or `.claude/rules/`, one subject each, such as `testing.md` or `go.md`. They also go into the system prompt on every turn and add up across layers. [AGENTS.md and rules](docs/rules.md#rules)

**Skills.** A skill is a directory with a `SKILL.md` file, which holds YAML frontmatter with a `name` and `description` and then the procedure. The model sees every skill's name and description each turn and loads the full text with `useSkill` when a task fits. Write `:skill-name` in a message and the skill arrives as its own message right after yours, also in a message sent while a turn runs. Peen ships four core skills, `core:planning`, `core:freshness`, `core:delegation`, and `core:plan-and-delegate`, and your own skills sit beside them. [Skills](docs/skills.md)

**Named agents.** A file in `.agents/agents/` defines a child agent with its own instructions and an optional `allowed-tools` list. The main agent hands it a job with `launchAgent`, and the child runs beside it. The two can exchange messages, the child can ask a question, and you can write to a running child or stop it. The child's final answer comes back when it ends. Every child run is stored and can be inspected. Peen ships one core agent, `core:general-purpose`, which runs every turn unless `PEEN_AGENT` names another. [Named agents](docs/agents.md)

**Session events.** When a background command finishes, a child agent ends, a hook reports something, or an outside system posts to `/api/v1/session/notices`, Peen records an event and hands it to the model at the next turn or tool boundary. A handler in `.agents/events/<type>.md` can make an event start a turn on an idle session. [Session events](docs/events.md)

**Hooks.** `.agents/hooks.yaml` runs actions at 38 points in a turn, before and after each message, turn, compaction, and tool call. An action can block the operation, run a command, add context for the model, or publish an event. [Hooks](docs/hooks.md)

## See what happened

The WebSocket is for live work, and REST is for stored reads and control. A message for the agent goes over the WebSocket only. The one REST call that can start a turn is posting a notice that an event handler lets wake an idle session.

```bash
curl "http://127.0.0.1:8080/api/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://127.0.0.1:8080/api/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

Add `-H "Authorization: Bearer <token>"` when `PEEN_API_TOKEN` is set. REST also lists session state, stored protocol events, outside notices, process output, child-agent runs, compaction history, and every model request and response. REST names a session with `X-Session-ID`, so a client keeps the IDs of the conversations it owns. [The API reference](docs/http-api.md) has every request and response.

## Drive it from the command line

Run one turn in the current directory with the installed `peen` command:

```bash
peen --prompt "fix the tests"
peen --model provider/model --output text --prompt "explain this project"
printf '%s\n' 'review the current changes' | peen --prompt -
```

Peen opens or resumes the directory's durable session, sends the prompt over WebSocket, streams the session's frames, and exits when the turn ends. It reuses a controller answering at `PEEN_HTTP_LISTEN_ADDRESS` and leaves it running. With none running, it starts a controller inside the command, serves the API through an in-memory listener with no TCP or metrics port, and stops it after the turn. A prompt sent during an active turn is queued into that turn, and the command follows it to its end. Other connected clients see the same work.

Output defaults to JSONL, preserving every WebSocket frame on stdout. `--output text` puts reply text on stdout and thinking, tools, child agents, and failures on stderr. Logs go to stderr at `warn` unless `LOG_LEVEL` is set. Exit status is 0 for completion, 1 for failure, and 130 for cancellation. The first interrupt requests cancellation and waits for confirmation, the second stops streaming and shuts down, and the third exits immediately. A question without a terminal available to answer cancels the turn.

A word that is not a `peen` command is refused, so a mistyped command never sends a prompt. The server binary takes `peen exec [flags] (--prompt <prompt> | <prompt>)`, and the installed command passes `peen exec` through too. The prompt is the `--prompt` value or the arguments joined with spaces, not both, and a prompt of `-` is read from stdin. Use `--workspace` for another directory, `--model` and `--reasoning-effort` for this turn's model settings, and `--system-prompt` with `--system-prompt-mode append|replace` for instructions. `--agent-space control|docker|docker:<profile>` selects the session's agent space and moves an existing session when needed; `docker` means `docker:default`. Defaults come from your configuration. The installed wrapper also accepts `--control-space host|docker` for this run. Exported `PEEN_*` and logging settings override its `.env` for the run, except `PEEN_HOME`, `PEEN_INSTALL_*`, and `PEEN_NO_BROWSER`.

The Peen server binary is also a local control client. With the installer, the `peen` command passes `session` and `control` to that binary with your installation's configuration. These commands talk to a running controller over its HTTP API:

```bash
peen control status
peen session open ~/work/my-app
peen session list
peen session attach <session-id>
peen session stop <session-id>
```

`peen control status` reports whether the controller answers and starts nothing, so it tells "not running" apart from "running". `peen session open` prints the session ID, its workspace, and whether the call created the session or resumed one. `peen session attach` streams that session's live events, starts no turn, and reconnects automatically after an unexpected drop, catching up from the session feed; `--cursor <n>` replays from sequence `n` first. `peen session stop` pauses unfinished session work and says when there was nothing to stop. Through the `peen` command, the `session` commands start the controller first when it is not running.

A server binary you run yourself, from `make install` or a source build, takes the same commands and reads the same `PEEN_` environment as the controller, so a command and its controller agree on the address. Its `session` commands start `peen run` themselves when nothing answers. The commands reach the controller over HTTP only, not over a Unix socket.

## Things worth knowing

A session with no client attached keeps running. Nothing but Stop, cancelling a child, shutting down the controller or an embedded `Close`, a failed turn, or a crash stops it, so an unattended session keeps calling the model and spending provider budget until something explicitly stops it.

Set `PEEN_API_TOKEN` and use `wss://` outside local development. Browser WebSockets authenticate with subprotocols, because browsers cannot attach an `Authorization` header. The [API reference](docs/http-api.md) has the handshake.

Peen writes structured logs to stdout and keeps daily audit files under `PEEN_STATE_DIR/logs` by default. The active workspace is a default, not a boundary, and an absolute tool path can still point outside it. Long conversations either drop old request context or replace it with a stored summary, as [compaction](docs/configuration.md#compaction) explains. [Configuration](docs/configuration.md) covers the rest.

## Security

Read this before you deploy Peen anywhere it can reach something you do not want touched.

A session's tools run in a worker process in the agent space the operator picked for that session. In the `control` agent space that worker is a child of the controller, so `runCommand` and the file tools have the same access as the operating-system user running Peen. There is no path allowlist, no secret-file denylist, and no approval or permission step before a tool runs. A file tool can read, write, or remove any path that user can touch, including `.git/`, `.env`, and SSH keys. `runCommand` runs whatever shell command the model gives it, at once. Peen is a coding agent with real access, and this is how it gets that access.

Commands the agent runs start with an environment that has `PEEN_API_TOKEN` and every provider `apiKeyEnv` variable removed. On Linux the controller and its workers are non-dumpable, so a command cannot read those values from their `/proc/<pid>/environ` or memory. A `control` agent still runs as the controller's user, so it can read and change `PEEN_STATE_DIR`, which holds every session's transcript, and it can read any file or other process of that user that holds a secret. Root and processes with `CAP_SYS_PTRACE` can still read a non-dumpable process. See [Where it runs](docs/where-it-runs.md#agent-spaces) for the full list.

A `docker:<profile>` agent space is the isolation boundary. It runs the worker in its own container with only the mounts, network, and grants the operator defined. A client picks an agent space by name and never sends an image, mount, network setting, or capability, so how much an agent can reach is the operator's decision. If the controller cannot reach a Docker daemon, a session in a Docker agent space is refused rather than run on the host. If it can, Peen refuses the `control` agent space, because a reachable daemon makes `control` sessions host root. See [Where it runs](docs/where-it-runs.md).

A worker container receives the workspace, `PEEN_CONFIG_DIR` read-only, its own session directory, and only the runtime configuration it needs, including the named provider credential for its model calls. It does not receive `PEEN_STATE_DIR`, the API token, or the controller's Docker socket. A profile with `docker: full` gets that socket, and one with `docker: readonly` gets a Docker API that answers only read requests. The agent's commands run as the same user as the worker process that holds the provider credential. The worker is non-dumpable, so they cannot read the credential from the worker's `/proc` files, but an agent with root in the container, as `sudo: true` gives, may reach it. Peen runs `PEEN_WORKER_IMAGE` when it is set, then the Docker profile's `image`, then the image published alongside the running build. The container starts as root and the Peen entrypoint drops it back to your host account, so an image without that entrypoint fails when the worker starts.

Tool calls and their results are recorded verbatim in the session transcript and sent live over the global WebSocket feed, like any other message. If the agent reads a file containing a secret, or a command prints one to stdout, that secret is now in the SQLite transcript and in every connected client that did not ask for one session only. Peen does not scan for or redact secret-shaped content in tool output. Treat the transcript and the event stream as sensitive as the files and commands the agent can reach.

`removePath` has one built-in restriction, a guard against a typo rather than a permission system. It refuses to remove the filesystem root, the session's original workspace directory, or the agent's current directory. Every other path, including everything named above, can be removed.

Run Peen as a non-root user, in a container, with only the mounts, network access, and capabilities the deployment needs. The installer's default does this, and [Docker deployment](#docker-deployment) below says where to read more.

## Docker deployment

The installer's default runs Peen from the `psyb0t/peen` image. The image has a real shell and the tools a coding agent uses, and the `peen` command runs it as your UID and GID, so files the controller and its workers write keep your ownership. To run the image yourself with `docker run`, Compose, or another process manager, and for production mounts, networking, and container hardening, read [Deployment](docs/deployment.md#docker).

## Agent integrations

The `peen` agent skill teaches an agent how to install and run Peen, send work over WebSocket, add workspace harness layers, and inspect stored state. It is documentation only. Installing it does not start a server, run a hook, or change a workspace.

Install it from the `psyb0t/agents` marketplace:

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
| Install Peen, use the `peen` command, or run it by hand | [Deployment](docs/deployment.md) |
| Decide where the agent runs and what it can touch | [Where it runs](docs/where-it-runs.md) |
| Configure providers, limits, and logs | [Configuration](docs/configuration.md) |
| Understand how Peen reads project files | [The harness](docs/harness.md) |
| Give the agent project instructions and rules | [AGENTS.md and rules](docs/rules.md) |
| Write named procedures the agent loads on demand | [Skills](docs/skills.md) |
| Define child agents for contained jobs | [Named agents](docs/agents.md) |
| React to background jobs, child agents, and outside systems | [Session events](docs/events.md) |
| Add hard checks or model instructions around actions | [Hooks](docs/hooks.md) |
| Build a WebSocket or REST client | [API reference](docs/http-api.md), [api/api.yml](api/api.yml), and [api/asyncapi.yaml](api/asyncapi.yaml) |
| Embed Peen in a Go process | [Go embedding guide](pkg/peen/README.md) |
| Change Peen itself | [Development](docs/development.md) |

## What Peen is built with

- [Elelem](https://github.com/psyb0t/elelem) talks to model providers, and [Essessey](https://github.com/psyb0t/essessey) rebuilds streamed content, tool calls, and thinking into stored conversation state.
- [Aichteeteapee](https://github.com/psyb0t/aichteeteapee) provides the HTTP server, REST error envelope, and WebSocket hub.
- [Servicepack](https://github.com/psyb0t/servicepack) runs the process and its services. Its own docs live in that repository, and this one documents Peen.
- [Gonfiguration](https://github.com/psyb0t/gonfiguration), [common-go](https://github.com/psyb0t/common-go), [commander](https://github.com/psyb0t/commander), [ctxerrors](https://github.com/psyb0t/ctxerrors), [ctxscope](https://github.com/psyb0t/ctxscope), [slogging](https://github.com/psyb0t/slogging), and [goenv](https://github.com/psyb0t/goenv) handle configuration, common utilities, process jobs, errors, scoped logs, log setup, and runtime environment detection.
- [go-workflows](https://github.com/cschleiden/go-workflows) schedules durable model, tool and hook activities. [GORM](https://github.com/go-gorm/gorm) and [SQLite](https://sqlite.org/) hold stored state.
- [Prometheus' Go client](https://github.com/prometheus/client_golang), [Cobra](https://github.com/spf13/cobra), [kin-openapi](https://github.com/getkin/kin-openapi), and [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) provide metrics, the command line, OpenAPI validation, and generated API clients.
