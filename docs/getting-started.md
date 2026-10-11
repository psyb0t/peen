# Getting started

Peen is a coding-agent backend that works in a real directory. It keeps the conversation, tool calls, protocol events, child-agent work, and provider exchanges after a client disconnects. You bring the workspace and the client. Peen runs the agent.

This guide gets an agent working in one project without handing it the rest of your machine. Peen serves its own browser control surface at the controller URL, and the same WebSocket and REST API for any other client.

## What you need

- Linux.
- Docker, with your user able to run `docker info`. The default setup below runs Peen and each agent in containers.
- A model provider API key.
- An existing project directory you are comfortable letting an agent inspect and change.
- A browser.

## 1. Install Peen

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
```

This installs the `peen` command in `~/.local/bin` and writes your configuration to `~/.config/peen`. It runs Peen in a container and gives each workspace's agent its own container. That container sees the project you open, Peen's configuration directory read-only, and the network Docker gives it, which reaches your LAN and the internet. [Deployment](deployment.md#install) lists the flags for the other setups, including one without Docker, and [where it runs](where-it-runs.md) says what the agent can touch in each one.

## 2. Configure a model provider

```bash
peen config edit
```

`PEEN_UPSTREAMS` gives each provider a local name, and models use that name, such as `zai/glm-5.3` or `aigate/your-model-id`. The file starts with two providers, [AIGate](https://github.com/psyb0t/aigate) and Z.ai, and a key variable for each. Keep the one you use, set its endpoint and its key variable, and set `PEEN_DEFAULT_MODEL` and `PEEN_COMPACTION_MODEL` to one of its models. Delete the other provider together with its key variable, because Peen refuses to start while a named key variable is missing or empty. [Providers](configuration.md#providers) describes each field.

`.env` is Docker `--env-file` input. Its provider value is raw JSON, so do not source this file from Bash. Keep it out of version control. `peen doctor` checks the file and the installation.

## 3. Open a workspace chat

```bash
cd ~/work/my-app
peen
```

`peen` starts Peen, opens the session for the current directory, and prints a link like `http://127.0.0.1:8080/?session=<id>`, opening it in your browser when it can. If Peen does not come up, `peen` prints the controller's last log lines, which usually name a provider or key problem.

The workspace root is your home directory unless you installed with `--workspace-root`. Peen may open any directory inside it, and each one becomes a workspace with its own session. Peen refuses a workspace that contains its own configuration or state directory, which are under `~/.config/peen`, so it refuses your home directory itself and opens the project directories inside it. Peen resumes the same session on a later start, so running `peen` again in the same directory returns to it.

On the page, enter `PEEN_API_TOKEN` if you set one, and connect. The linked session opens. To open another project from the page, type a directory inside the workspace root in the sidebar, such as `$HOME/work/other-app`, and open the chat. Peen rejects a path that is missing, is not a directory, or is outside the root, and shows the reason.

Type in the full-width input box. Enter sends, and Shift+Enter starts a new line. Your messages show as bubbles on the right. The reply takes the full width with no bubble and streams in as the model writes it, rendered as Markdown, with reasoning collapsed and each tool call shown as one card next to its result. A message you send while a turn is running shows "Queued. Lands after the current step." until the agent receives it. The Details panel holds the full stored record when you need it.

Opening the same directory again returns the same session, so this is also how you reattach after a restart. Every connected browser receives live events for every session, then filters them into workspace tabs by `metadata.sessionId`.

If you are writing another client, `POST /v1/sessions/open` returns the session ID, and `GET /v1/workspace-roots` returns the workspace root in a list. Send later turns through the global WebSocket with that ID in `metadata.sessionId`. [The API reference](http-api.md) has the frame shapes.

## 4. Put project instructions beside the project

Start with an `AGENTS.md` at the workspace root, the instructions file in the [agents.md](https://agents.md) format. Write the things an agent needs to know every time: how to build, where tests live, what it must not touch, and local conventions. Add the optional `.agents` directory as the work gets more specific:

```text
workspace/
  AGENTS.md
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

Peen also reads these files in every parent directory of the workspace, so `~/work/AGENTS.md` applies to every project under `~/work` and the project's own `AGENTS.md` adds to it. An `AGENTS.md` in a subdirectory of the workspace covers that directory, and the closest one wins. Put topic rules that must apply to every turn in `.claude/rules/*.md` or `.agents/rules/*.md`.

Skills give the agent named procedures. Every turn sees each skill's name and description, and the model loads a skill with `useSkill` when a task matches it. Write `:skill-name` at the start of a message or after a space to send that skill yourself, as a message right after yours. When two layers define a skill with the same name, the one closest to the workspace is used. Named agents let the agent hand off a bounded job. Hooks are for mechanical checks and hard stops that a prompt should not be trusted to enforce. Each one has its own guide: [the harness](harness.md), [rules](rules.md), [skills](skills.md), [named agents](agents.md), [session events](events.md), and [hooks](hooks.md).

Peen checks every optional rule, skill, named agent, event handler, and hook on its own. If one is malformed, Peen keeps the rest, logs a warning, records a `harness.warning` event, and shows the file and the reason in the chat. Fix the reported file when you need that definition. A `:skill-name` that names a skill Peen skipped this way fails with an error instead of sending nothing.

## 5. Inspect or stop a run

WebSocket starts work. REST reads stored state and controls an active session. The one REST call that can start a turn is posting a [session notice](events.md) that an event handler lets wake an idle session.

```bash
curl "http://localhost:8080/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://localhost:8080/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

Add `-H "Authorization: Bearer <token>"` to each request when you set `PEEN_API_TOKEN`. The API also exposes stored protocol events, outside notices, process jobs and their output and signal history, child-agent runs, turns, prompt and context snapshots, compaction lineage, provider runs and rounds, and session status. [The API reference](http-api.md) has the frame and response shapes.

## Before you expose it

Set `PEEN_API_TOKEN` before putting Peen on a network you do not fully trust. Use `wss://` outside local development. Mount only the directories the agent needs, run as a non-root user, and treat the transcript as sensitive, because tool output and files an agent reads can end up in it.

[Deployment](deployment.md) covers the installer, source builds, mounts, networking, and the container boundary. [Configuration](configuration.md) lists every setting. The [README](../README.md) is the short version.
