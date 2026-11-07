# peen

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/peen.svg)](https://pkg.go.dev/github.com/psyb0t/peen)
[![CI](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/peen/badges/coverage.svg)](https://github.com/psyb0t/peen/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/peen/badges/version.svg)](https://github.com/psyb0t/peen/releases)
[![license](https://raw.githubusercontent.com/psyb0t/peen/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/peen/badges/importers.svg)](https://github.com/psyb0t/peen/blob/badges/importers.md)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/peen?style=flat-square)](https://hub.docker.com/r/psyb0t/peen)

_peen goes in vageen_

**Documentation: [psyb0t.github.io/peen](https://psyb0t.github.io/peen/)**

Peen is a coding agent built as a server. One control server opens a session for each workspace directory and runs Peen's agent for that session in its own worker, either a process beside the controller or a separate Docker container. Clients send work and watch it live over WebSocket, and the agent keeps working when they disconnect. It can read code, edit files, run commands, use skills, launch child agents, and follow the `AGENTS.md` and rules sitting beside the project.

Every conversation, tool call, agent event, context snapshot, compaction, and provider exchange lands in SQLite. Model records keep the request and response, thinking, usage, retries, cost, model identity, and connection name, never the provider credential. Reconnect after a restart and the history is still there. A global WebSocket receives every session's live feed; a session-filtered socket receives only that session.

Root and nested agent conversations run through SQLite-backed go-workflows. After a controller or worker crash, Peen restores saved context and reuses completed activity results. Stop pauses unfinished work, and reconnecting leaves it paused. Ask an agent to inspect and explicitly continue the retained task; uncertain tool effects need inspection before retry. See [recovery and continuation](https://psyb0t.github.io/peen/agents/#recovery-after-a-crash).

One host runs one control plane. It owns SQLite, the API, and the event feed, and the operator chooses whether workers run as child processes or containers. See [Where it runs](https://psyb0t.github.io/peen/where-it-runs/) and [Architecture](https://psyb0t.github.io/peen/architecture/).

Peen ships its own browser [control surface](https://psyb0t.github.io/peen/control-surface/) at the controller's root URL. It is one client among many, so a terminal client, a bot, or your own application can watch the same live work and use the same stored records.

## Contents

- [Install](#install)
- [Run it](#run-it)
- [Security](#security)
- [GitHub action](#github-action)
- [Agent integrations](#agent-integrations)
- [Documentation](#documentation)

## Install

You need Linux, a model provider API key, and, for the default setup, Docker that your user can run.

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

Read [install.sh](install.sh) before piping it into a shell. It installs the `peen` command in `~/.local/bin`, pins the newest release image, and writes your configuration to `~/.config/peen/.env`.

By default Peen runs in a container and gives each workspace's agent its own container. [Where it runs](https://psyb0t.github.io/peen/where-it-runs/) says what the agent can touch in each setup, and [Deployment](https://psyb0t.github.io/peen/deployment/#install) lists every installer flag, the `peen` commands, and the manual routes.

## Run it

Add your model provider first. Peen refuses to start until the provider that serves your default model answers and every key the file names is set.

```bash
peen config edit
```

Keep the provider you use, delete the other entry together with its key variable, and set both models to one of its models. For an OpenAI-compatible [AIGate](https://github.com/psyb0t/aigate) setup, the important lines look like this:

```dotenv
PEEN_UPSTREAMS=[{"name":"aigate","type":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"}]
PEEN_DEFAULT_MODEL=aigate/your-model-id
PEEN_COMPACTION_MODEL=aigate/your-model-id
AIGATE_TOKEN=your-token-here
PEEN_API_TOKEN=
```

The file is a Docker `--env-file`, so leave the JSON unquoted. For a server outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting it. `peen doctor` checks the result. [Configuration](https://psyb0t.github.io/peen/configuration/) lists every setting.

Then open a project:

```bash
cd ~/work/my-app
peen
```

`peen` starts Peen when it is not running, opens the session for the current directory, and prints its link, `http://127.0.0.1:8080/?session=<id>`, opening it in your browser when it can. Run it again in the same directory and you get the same session back, with its history. To run one turn from the shell instead:

```bash
peen --prompt "fix the tests"
```

[Getting started](https://psyb0t.github.io/peen/getting-started/) walks through the first run, and the [command line guide](https://psyb0t.github.io/peen/command-line/) has every flag.

## Security

The agent's file tools and `runCommand` have the full access of the account its worker runs as, with no path allowlist and no approval step. A Docker agent space is the isolation boundary. Tool output is stored and streamed verbatim, unredacted. A session with no client attached keeps running and spending provider budget until something stops it. Set `PEEN_API_TOKEN` and use `wss://` outside local development. Read [Security](https://psyb0t.github.io/peen/security/) before you deploy Peen anywhere it can reach something you do not want touched.

## GitHub action

Run a prompt as a workflow step on a Linux runner with Docker. Your workflow prepares the directory, including checkout when needed. Peen runs the prompt, streams its reply, and returns `result` and `result-file` outputs.

```yaml
- uses: psyb0t/peen@main
  id: peen
  with:
    prompt: Explain this project and write a summary to SUMMARY.md.
    working-directory: .
    model: gateway/your-model-id
    upstreams: >-
      [{"name":"gateway","type":"openai","baseUrl":"https://gateway.example/v1","apiKeyEnv":"PROVIDER_KEY"}]
  env:
    PROVIDER_KEY: ${{ secrets.PROVIDER_KEY }}
```

Replace the provider URL and model, and add `PROVIDER_KEY` to your repository's Actions secrets. `working-directory` can name any prepared directory, including one outside a checkout. The [GitHub action guide](https://psyb0t.github.io/peen/github-action/) has complete workflows with and without checkout, output handling, and every input.

## Agent integrations

The `peen` agent skill teaches an agent how to install and run Peen, send work over WebSocket, add workspace harness layers, and inspect stored state. [Agent integrations](https://psyb0t.github.io/peen/agent-integrations/) has the install commands for Claude Code, Codex, and OpenClaw.

## Documentation

The full documentation is at [psyb0t.github.io/peen](https://psyb0t.github.io/peen/), with search. The source is in [`docs/`](docs/).

| You want to | Read |
| --- | --- |
| Start from zero | [Getting started](https://psyb0t.github.io/peen/getting-started/) |
| Install Peen, use the `peen` command, or run it by hand | [Deployment](https://psyb0t.github.io/peen/deployment/) |
| Run turns and manage sessions from a shell | [Command line](https://psyb0t.github.io/peen/command-line/) |
| Run a prompt in a GitHub workflow | [GitHub action](https://psyb0t.github.io/peen/github-action/) |
| Use the browser chat | [Control surface](https://psyb0t.github.io/peen/control-surface/) |
| Decide where the agent runs and what it can touch | [Where it runs](https://psyb0t.github.io/peen/where-it-runs/) |
| Know what the agent can reach before you deploy | [Security](https://psyb0t.github.io/peen/security/) |
| Configure providers, limits, and logs | [Configuration](https://psyb0t.github.io/peen/configuration/) |
| See what the agent can do | [Agent tools](https://psyb0t.github.io/peen/tools/) |
| Understand how Peen reads project files | [The harness](https://psyb0t.github.io/peen/harness/) |
| Give the agent project instructions and rules | [AGENTS.md and rules](https://psyb0t.github.io/peen/rules/) |
| Write named procedures the agent loads on demand | [Skills](https://psyb0t.github.io/peen/skills/) |
| Define child agents for contained jobs | [Named agents](https://psyb0t.github.io/peen/agents/) |
| React to background jobs, child agents, and outside systems | [Session events](https://psyb0t.github.io/peen/events/) |
| Add hard checks or model instructions around actions | [Hooks](https://psyb0t.github.io/peen/hooks/) |
| Give the agent the tools of MCP servers | [MCP servers](https://psyb0t.github.io/peen/mcp/) |
| Build a client | [WebSocket API](https://psyb0t.github.io/peen/websocket-api/), [REST API](https://psyb0t.github.io/peen/http-api/), [api/api.yml](https://github.com/psyb0t/peen/blob/main/api/api.yml), and [api/asyncapi.yaml](https://github.com/psyb0t/peen/blob/main/api/asyncapi.yaml) |
| Embed Peen in a Go process | [Go embedding guide](https://psyb0t.github.io/peen/embedding/) |
| Fix something that broke | [Troubleshooting](https://psyb0t.github.io/peen/troubleshooting/) |
| See how it fits together and what it is built with | [Architecture](https://psyb0t.github.io/peen/architecture/) |
| See how it does on coding benchmarks and what they found | [Benchmarks](https://psyb0t.github.io/peen/benchmarks/) |
| Change Peen itself | [Development](https://psyb0t.github.io/peen/development/) |
