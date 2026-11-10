---
title: peen
description: Documentation for Peen, a self-hosted coding agent server. Install it, connect a model provider, and run agents on your projects from a browser, a shell, or an API.
nav_order: 1
permalink: /
seo:
  type: WebSite
---

# peen documentation

Peen is a coding agent built as a server. One controller opens a session for each workspace directory and runs the agent for that session in its own process or Docker container. Clients send work and watch it live over WebSocket, and the agent keeps working when they disconnect.

Every conversation, tool call, event, compaction, and provider exchange lands in SQLite, so you can reconnect after a restart and read what happened. After a crash, Peen restores saved context and reuses completed activity results.

The source is at [github.com/psyb0t/peen](https://github.com/psyb0t/peen).

## Install

You need Linux, a model provider API key, and, for the default setup, Docker that your user can run.

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

Read [install.sh](https://github.com/psyb0t/peen/blob/main/install.sh) before piping it into a shell. [Getting started](getting-started.md) walks through the first run.

## Find what you need

| You want to | Read |
| --- | --- |
| Start from zero | [Getting started](getting-started.md) |
| Install Peen, use the `peen` command, or run it by hand | [Deployment](deployment.md) |
| Decide where the agent runs and what it can touch | [Where it runs](where-it-runs.md) |
| Know what the agent can reach before you deploy | [Security](security.md) |
| Configure providers, limits, and logs | [Configuration](configuration.md) |
| Run turns and manage sessions from a shell | [Command line](command-line.md) |
| Use the browser chat | [Control surface](control-surface.md) |
| Run a prompt in a GitHub workflow | [GitHub action](github-action.md) |
| See what the agent can do | [Agent tools](tools.md) |
| Teach the agent about your project | [The harness](harness.md) |
| Build a client | [WebSocket API](websocket-api.md) and [REST API](http-api.md) |
| Embed Peen in a Go process | [Embedding Peen](embedding.md) |
| Let another agent operate Peen | [Agent integrations](agent-integrations.md) |
| Fix something that broke | [Troubleshooting](troubleshooting.md) |
| See how it fits together | [Architecture](architecture.md) |
| See how it does on coding benchmarks | [Benchmarks](benchmarks.md) |
| Change Peen itself | [Developing Peen](development.md) |
