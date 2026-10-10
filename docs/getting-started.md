# Getting started

Peen is a coding-agent backend that works in a real directory. It keeps the
conversation, tool calls, protocol events, child-agent work, and provider
exchanges after a client disconnects. You bring the workspace and the client.
Peen runs the agent.

This gets an agent working in one folder without handing it the rest of your
machine. Peen serves its own browser control surface at the controller URL. It
also exposes the same WebSocket and REST surfaces for another client.

## What you need

- Docker.
- A provider API key.
- An existing project directory you are comfortable letting an agent inspect
  and change.
- A browser.

## 1. Configure Peen

Create separate configuration, state, and workspace root directories, then
download the example configuration and edit the provider section:

```bash
root="$HOME/.local/share/peen"
config="$root/config"
state="$root/state"
workspace_root="$HOME/work"
mkdir -p "$config" "$state" "$workspace_root/my-app"
curl -fsSLo "$root/.env" https://raw.githubusercontent.com/psyb0t/peen/main/.env.example
${EDITOR:-vi} "$root/.env"
```

`PEEN_UPSTREAMS` gives each provider a local name. Models use that name, such
as `zai/glm-5.3` or `aigate/your-model-id`. Set the provider endpoint, the
named key variable, and the two model variables. The default example includes
both [AIGate](https://github.com/psyb0t/aigate) and Z.ai. Delete every entry
you do not use, together with its key variable. Peen contacts every listed
provider at startup, and refuses to start when a named key variable is missing
or empty. [Providers](configuration.md#providers) describes each field.

`.env` is Docker `--env-file` input. Its provider value is raw JSON, so do not
source this file from Bash. Keep it out of version control.

## 2. Start it with one workspace

Run the published image. The controller and any Docker worker must see each
host directory at the same literal path.

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

To build the image from a checkout instead, see [Deployment](deployment.md#docker).

`workspace_root` is the workspace root: the controller may open it or any directory inside it. Put the projects you want the agent to work on inside it, or point the variable at a directory that already holds them. Each project you open becomes a workspace with its own session. `config` holds the trusted harness layer. `state` holds SQLite, logs, and worker sockets. The Docker processes use your UID and GID, so agent writes keep host ownership. Peen resumes the same session on a later start with the same state directory and workspace. Do not mount your whole home directory because the agent has normal file and command access inside its container.

## 3. Open a workspace chat

Visit `http://localhost:8080`. Enter `PEEN_API_TOKEN` when you set one. The sidebar shows the workspace root; type a project directory inside it, such as `$HOME/work/my-app`, and open the chat. The controller remains the authority. It rejects a missing, non-directory, or outside-root path with a clear error. Type in the full-width input box. Enter sends, and Shift+Enter starts a new line. Your messages show as bubbles on the right. The reply takes the full width with no bubble and streams in as the model writes it, rendered as Markdown, with reasoning collapsed and each tool call shown as one card next to its result. A message you send while a turn is running shows "Queued. Lands after the current step." until the agent receives it. The Details panel holds the full persisted record when you need it.

Opening the same directory again returns the same session, so this is also how
you reattach after a restart. Every connected browser receives live events for
every session, then filters them into workspace tabs by `metadata.sessionId`.

If you are writing another client, `POST /v1/sessions/open` returns the session
ID and `GET /v1/workspace-roots` lists the roots it may offer. Send later turns
through the global WebSocket with that ID in `metadata.sessionId`. [The API
reference](http-api.md) has the exact frame shape.

## 4. Put project instructions beside the project

Start with an `AGENTS.md` at the workspace root, the instructions file in the
[agents.md](https://agents.md) format. Write the things an agent needs to know
every time: how to build, where tests live, what it must not touch, and local
conventions. Add the optional `.agents` directory as the work
gets more specific:

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

Peen also reads these files in every parent directory of the workspace, so
`~/work/AGENTS.md` applies to every project under `~/work` and the project's
own `AGENTS.md` adds to it. An `AGENTS.md` in a subdirectory of the workspace
covers that directory, and the closest one wins. Put topic rules that must
apply to every turn in
`.claude/rules/*.md` or `.agents/rules/*.md`. Skills give the agent named
procedures. Every turn sees a skill's name and description, then the model
decides whether an ordinary task matches and loads it with `use_skill`. Put a
standalone `:skill-name` at the start of a message or after whitespace to
require the effective skill by exact name. Named agents let it split off a
bounded job. Hooks are for mechanical checks and hard stops that an ordinary
prompt should not be trusted to enforce. Each one has its own guide:
[the harness](harness.md), [rules](rules.md), [skills](skills.md),
[named agents](agents.md), [session events](events.md), and [hooks](hooks.md).

Peen validates every optional rule, skill, named agent, event handler, and hook
independently. If one is malformed, Peen keeps the valid configuration, logs a
warning, records a durable `harness.warning` event, and shows the exact source
and validation reason in the chat. Fix the reported file when you need that
definition. A direct `:skill-name` reference still fails clearly when the
requested skill was ignored.

## 5. Inspect or stop a run

WebSocket starts work. REST reads durable state and controls an active session.
It never creates a turn.

```bash
curl "http://localhost:8080/v1/messages?limit=50&order=asc" \
  -H "X-Session-ID: <session-id>"

curl -X POST "http://localhost:8080/v1/session/cancel" \
  -H "X-Session-ID: <session-id>"
```

The API also exposes durable protocol events, outside notices, process jobs and
their output and signal history, child-agent runs, turns, prompt and context
snapshots, compaction lineage, provider runs and rounds, and session status.
[The API reference](http-api.md) has the exact frame and response shapes.

## Before you expose it

Set `PEEN_API_TOKEN` before putting Peen on a network you do not fully trust.
Use `wss://` outside local development. Mount only the directories the agent
needs, run as a non-root user, and treat the transcript as sensitive. Tool
output and files an agent reads can end up in it.

[Deployment](deployment.md) covers source builds, mounts, networking, and
the container boundary. [Configuration](configuration.md) lists every
setting. The root [README](../README.md) is the short version.
