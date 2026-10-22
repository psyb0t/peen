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

Peen ships its own browser [control surface](docs/control-surface.md) at the controller's root URL. It is one client among many, so a terminal client, a bot, or your own application can watch the same live work and use the same stored records.

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

By default Peen runs in a container and gives each workspace's agent its own container. [Where it runs](docs/where-it-runs.md) says what the agent can touch in each setup, and [Deployment](docs/deployment.md#install) lists every installer flag, the `peen` commands, and the manual routes.

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

The file is a Docker `--env-file`, so leave the JSON unquoted. For a server outside your own machine, set `PEEN_API_TOKEN` to a real secret before starting it. `peen doctor` checks the result. [Configuration](docs/configuration.md) lists every setting.

Then open a project:

```bash
cd ~/work/my-app
peen
```

`peen` starts Peen when it is not running, opens the session for the current directory, and prints its link, `http://127.0.0.1:8080/?session=<id>`, opening it in your browser when it can. Run it again in the same directory and you get the same session back, with its history. To run one turn from the shell instead:

```bash
peen --prompt "fix the tests"
```

[Getting started](docs/getting-started.md) walks through the first run, and the [command line guide](docs/command-line.md) has every flag.

## Security

The agent's file tools and `runCommand` have the full access of the account its worker runs as, with no path allowlist and no approval step. A Docker agent space is the isolation boundary. Tool output is stored and streamed verbatim, unredacted. A session with no client attached keeps running and spending provider budget until something stops it. Set `PEEN_API_TOKEN` and use `wss://` outside local development. Read [Security](docs/security.md) before you deploy Peen anywhere it can reach something you do not want touched.

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

Replace the provider URL and model, and add `PROVIDER_KEY` to your repository's Actions secrets. `working-directory` can name any prepared directory, including one outside a checkout. The [GitHub action guide](docs/github-action.md) has complete workflows with and without checkout, output handling, and every input.

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
| Run turns and manage sessions from a shell | [Command line](docs/command-line.md) |
| Run a prompt in a GitHub workflow | [GitHub action](docs/github-action.md) |
| Use the browser chat | [Control surface](docs/control-surface.md) |
| Decide where the agent runs and what it can touch | [Where it runs](docs/where-it-runs.md) |
| Know what the agent can reach before you deploy | [Security](docs/security.md) |
| Configure providers, limits, and logs | [Configuration](docs/configuration.md) |
| Understand how Peen reads project files | [The harness](docs/harness.md) |
| Give the agent project instructions and rules | [AGENTS.md and rules](docs/rules.md) |
| Write named procedures the agent loads on demand | [Skills](docs/skills.md) |
| Define child agents for contained jobs | [Named agents](docs/agents.md) |
| React to background jobs, child agents, and outside systems | [Session events](docs/events.md) |
| Add hard checks or model instructions around actions | [Hooks](docs/hooks.md) |
| Build a WebSocket or REST client | [API reference](docs/http-api.md), [api/api.yml](api/api.yml), and [api/asyncapi.yaml](api/asyncapi.yaml) |
| Embed Peen in a Go process | [Go embedding guide](pkg/peen/README.md) |
| See how it fits together and what it is built with | [Architecture](docs/architecture.md) |
| Change Peen itself | [Development](docs/development.md) |
