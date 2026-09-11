# peen

Peen is one stateful coding agent, exposed through durable REST reads and a
WebSocket turn protocol. A client chooses a session UUID, sends messages over
one or more sockets, and receives the agent's native events on every connected
socket for that session. Every turn, message, and event is recorded in SQLite,
so a session survives a process restart. A message accepted into an already
active turn keeps its delivery state only in memory until its next provider
round; after a restart clients retry it.

Peen also acts as a small file-based agent harness. It starts with embedded
operating rules and skills, then resolves layered `AGENTS.md` files, Agent
Skills, and named child agents from the directories around the current
message's workspace. The agent reads and edits files and runs shell commands
with the same access as the operating-system user running the process: no
sandbox, no allowlist, no approval step. Read [Security](#security) before you
point this at anything you care about.

## Contents

- [Quick start](#quick-start)
- [Provider configuration](#provider-configuration)
- [The configuration directory](#the-configuration-directory)
- [HTTP API](#http-api)
- [Bearer authentication](#bearer-authentication)
- [Metrics](#metrics)
- [Workspaces](#workspaces)
- [Compaction modes](#compaction-modes)
- [Security](#security)
- [Docker deployment](#docker-deployment)
- [Further reading](#further-reading)

## Quick start

Requires Docker (the `make` targets build and test through Docker images) and
an OpenAI-compatible, Anthropic-compatible, or Z.ai Coding upstream to point
at.

```bash
cp .env.example .env
# Edit .env: set PEEN_UPSTREAMS, PEEN_DEFAULT_MODEL, and the API key
# environment variable PEEN_UPSTREAMS references.
```

Build a static binary with Docker and run it as a bare process. `.env.example`
defaults `PEEN_CONFIG_DIR` and `PEEN_WORKING_DIR` to container paths
(`/data/peen`, `/workspace`); for a bare process, point them at writable local
directories instead:

```bash
make build
set -a && source .env && set +a
export PEEN_CONFIG_DIR="$PWD/.peen/config"
export PEEN_WORKING_DIR="$PWD/.peen/workspace"
mkdir -p "$PEEN_CONFIG_DIR" "$PEEN_WORKING_DIR"
./build/peen run
```

Connect a WebSocket client to
`ws://localhost:8080/v1/ws?sessionId=<new-uuid>` and send this Dabluvee event:

```json
{"type":"message.send","data":{"message":"list the files in the current directory"}}
```

The socket streams native agent events followed by `message.completed`. The
chosen UUID becomes the durable session ID on the first message. See
[the WebSocket contract](docs/peen/http-api.md#websocket-turns) for framing,
authentication, and multi-client synchronization. See
[Docker deployment](#docker-deployment) for running the same thing as a
container with durable mounted state, and
[docs/peen/deployment.md](docs/peen/deployment.md) for `go install` and
source-build paths.

Other useful targets: `make test-unit`, `make test-integration`, `make
test-api` (containerized, exercises the real HTTP stack against a scripted
provider), `make test-execution-forms` (source and local-install process
contracts), `make test-real` (runs a paid, isolated end-to-end coding fixture
against `zai/glm-5.3-flash` by default), and `make lint`. Run `make help` for the
complete list.

`make test-real` starts the actual Peen binary inside its own Docker container,
with no Docker socket, a disposable layered workspace, a private SQLite state,
and a per-run API token. The live model must load a skill, launch a read-only
child agent, read and patch a small Go project, run its test, trigger hooks and
events, and satisfy durable transcript, audit-log, and outbound-context checks.
The forwarding check keeps only in-memory marker matches, so it never writes
prompts or credentials to logs. Set `PEEN_TEST_DEFAULT_MODEL` to run this
scenario against another configured `provider/model` without changing the
live-test default or deployment default.

## Provider configuration

`PEEN_UPSTREAMS` is a JSON array of named providers:

```json
[
  {"name":"aigate","provider":"openai","baseUrl":"https://aigate.example/v1","apiKeyEnv":"AIGATE_TOKEN"},
  {"name":"zai","provider":"zai-coding","baseUrl":"https://api.z.ai/api/coding/paas/v4","apiKeyEnv":"ZAI_TOKEN"}
]
```

Each entry has a unique `name`, a `provider` type (`openai`, `anthropic`, or
`zai-coding`), an optional `baseUrl`, and an optional `apiKeyEnv`.
`zai-coding` uses Z.ai's Coding endpoint and preserves its thinking state
through tool rounds. `apiKeyEnv` names an environment variable read at startup,
so the credential never appears in `PEEN_UPSTREAMS` itself, in a config dump,
or in a log line, and it can be rotated without touching the upstream list.

A model is addressed as `<upstream-name>/<model-id>`, split on only the first
slash, so a provider's own model ID can itself contain slashes. Peen never
invents or aliases a model name: at startup it builds one driver per upstream
and calls its `ListModels` API, and only IDs the provider actually returns get
registered. `PEEN_DEFAULT_MODEL` and `PEEN_COMPACTION_MODEL` (defaults to
`PEEN_DEFAULT_MODEL`) must each resolve to a discovered model whose context
window is at least `PEEN_MAX_CONTEXT_TOKENS`, or the process refuses to
start. A discovery failure on an upstream neither setting selects is logged,
and only that one upstream stays unavailable.

The `data` of a `message.send` WebSocket event accepts an optional per-message
`model` field that overrides the default for that one call. It must already be
a discovered `provider/model` reference; it is not sticky and does not change
what later messages in the same session use.

For Z.ai Coding, use `zai/glm-5.3` for the main coding turn and
`zai/glm-5.3-flash` for lightweight work. Peen does not infer task difficulty;
the caller selects the model for each turn.

The full list of tunables (context, tool, and event limits) is in
`.env.example` and [docs/peen/configuration.md](docs/peen/configuration.md).

## The configuration directory

`PEEN_CONFIG_DIR` holds both the harness's base layer and Peen's durable
state:

```text
<config-dir>/
  AGENTS.md
  SYSTEM.md
  APPEND_SYSTEM.md
  COMPACTION.md
  .agents/
    skills/<skill-name>/SKILL.md
    agents/<agent-name>.md
    events/<event-type>.md
    hooks.yaml
  hook-state/<session-id>/
  peen.db
```

Every file here is optional; a missing one is normal, not an error.

Peen always ships an embedded operating-rules block and `planning` and
`freshness` skills. It applies those before the configuration directory layer
without materializing them as files. A configuration or workspace skill with
the same name replaces its embedded version.

- `AGENTS.md`: project instructions applied to every turn. Peen keeps each
  layer's block in order, from the embedded base through the workspace layer.
- `SYSTEM.md`: replaces Peen's built-in default system prompt entirely.
- `APPEND_SYSTEM.md`: appended after whichever prompt is active.
- `COMPACTION.md`: replaces the built-in summarization prompt, read only when
  `PEEN_COMPACTION_MODE=summarize`; a change takes effect after restart.
- `.agents/skills/<name>/SKILL.md`: an Agent Skill, loaded in full through
  `use_skill` when the model needs it. A same-named later layer replaces an
  earlier skill.
- `.agents/agents/<name>.md`: a named child agent `launch_agent` can run by
  name. Its optional `allowed-tools` frontmatter is an enforced comma-separated
  tool allowlist.
- `.agents/events/<type>.md`: a handler for one session event type.
- `.agents/hooks.yaml`: ordered lifecycle actions that can deny an operation,
  run a direct executable, add model context, or publish a session event. See
  [docs/peen/hooks.md](docs/peen/hooks.md).
- `hook-state/<session-id>/`: private persistent state for command hooks in
  that session. Peen creates the directory tree with mode `0700`.
- `peen.db`: the SQLite database. Peen creates `PEEN_CONFIG_DIR` itself
  (mode `0700`) if it does not exist yet, and the database file at `0600`. It
  refuses a symlinked config directory or database path.

Full layering and file format details: [docs/peen/configuration.md](docs/peen/configuration.md).

## HTTP API

Every operation is under `/v1`. Full request and response shapes:
[docs/peen/http-api.md](docs/peen/http-api.md).

| Method | Path | Purpose |
| --- | --- | --- |
| WS | `/v1/ws?sessionId=<uuid>` | Submit agent turns and receive synchronized agent events. |
| GET | `/v1/messages` | List stored conversation messages, paginated. |
| GET | `/v1/session` | Read session details. |
| POST | `/v1/session/cancel` | Request cancellation of the active turn. |
| GET | `/v1/session/events` | List the session's pending events. |
| POST | `/v1/session/events` | Report an event to the session from outside. |
| GET | `/v1/session/jobs` | List the commands this session has started. |
| GET | `/v1/session/jobs/{jobId}/output` | Read a bounded window of one job's output. |
| POST | `/v1/session/jobs/{jobId}/signal` | Stop one running job. |
| GET | `/v1/session/agents` | List the child agent runs this session has started. |
| GET | `/v1/session/agents/{agentRunId}/messages` | Follow what one child agent run is doing. |
| POST | `/v1/session/agents/{agentRunId}/cancel` | Cancel one child agent run without ending its parent turn. |

Two probe endpoints sit outside `/v1` and outside authentication, because an
orchestrator health-checking the service has no bearer token to present:

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Liveness. Returns `200 {"status":"ok"}` while the process is serving. |
| GET | `/ready` | Readiness. Same answer, and that is accurate: the database is opened, migrated, and integrity-checked before the listener exists, so a process that failed any of those has no listener to probe. |

Every REST operation requires the `X-Session-ID` header and never creates a
session; an unknown session returns `404`. The WebSocket endpoint uses its
required `sessionId` query parameter instead, because browser WebSocket clients
cannot set `X-Session-ID`. A new UUID is accepted as a pending session, and its
first `message.send` atomically creates that exact session. Connections for the
same session share one hub client, so every connected browser or client receives
the same agent events. No HTTP endpoint accepts an agent message. See
[the WebSocket contract](docs/peen/http-api.md#websocket-turns).

## Bearer authentication

`PEEN_API_TOKEN` is empty by default, which disables authentication entirely.
Set it to any non-empty value to require every request to carry
`Authorization: Bearer <PEEN_API_TOKEN>`. The comparison is constant-time. A
missing or wrong token returns `401`.

Native browser WebSockets cannot set `Authorization`. For `GET /v1/ws`, they
instead send `peen.v1` and `peen.bearer.<base64url-token>` as subprotocols; the
server selects `peen.v1` and verifies the second value. Do not put the bearer
token in a query parameter. Use `wss://` outside local development.

`/healthz` and `/ready` are exempt. Everything under `/v1` is not.

## Metrics

Prometheus metrics are available at `GET /metrics` only through the separate
`PEEN_METRICS_LISTEN_ADDRESS` listener, which defaults to
`127.0.0.1:9090`. This listener is loopback-only, unauthenticated, and never
shares the public API surface. A scraper must run in Peen's network namespace.
See [the configuration reference](docs/peen/configuration.md#metrics).

## Logging

`LOG_LEVEL` controls the structured records Peen writes to stdout. The audit
sink retains debug-and-above records in
`PEEN_CONFIG_DIR/logs/YYYYMMDD-000000.log` by default, using UTC and keeping
14 daily files. Set `PEEN_LOG_DIRECTORY` to override it. Correlated audit
records show the resolved harness manifest, skill and hook activity,
child-agent lifecycle, and tool outcomes. They contain identifiers, sizes, and
SHA-256 digests, not raw prompts or tool output. Session storage and
child-agent JSONL transcripts hold the sensitive verbatim trace for authorized
debugging. ORM SQL statement previews are deliberately disabled because
expanded statements can contain persisted sensitive content.

## Workspaces

`PEEN_WORKING_DIR` is the default working directory for every message: Peen
changes into it at startup, and it is what a message's tools and relative
paths resolve against when no override is given.

A `workspace` field in a `message.send` event's `data` overrides it for that
one message only. It is never sticky: it does not change the session's default,
and the next message without a `workspace` field uses `PEEN_WORKING_DIR`
again. `~` expands to the running user's home directory, and the result must
be an existing, readable directory. The workspace is a default directory, not
a containment boundary: an absolute path outside it is accepted.

Before a file tool can overwrite, edit, move, delete, or patch an existing
regular file, that file's contents must have been read in the current turn.
Peen checks the observed content hash again under its mutation lock. A directory
listing does not count as reading the file. New files and move destinations are
checked for absence, then published with no-replace filesystem operations so a
concurrently created target is never silently overwritten. `edit_file` handles
exact replacements in one file. `apply_patch` handles strict coordinated
updates, additions, deletions, and moves across files.

## Compaction modes

`PEEN_MAX_CONTEXT_TOKENS` is the request budget Elelem enforces. When a
request would exceed it, `PEEN_COMPACTION_MODE` picks how Peen responds:

- `drop-oldest` (default): Elelem's built-in policy evicts the oldest
  complete conversation units from that one request. Nothing is written to
  the database, and the original transcript is untouched.
- `summarize`: Peen replaces an old, completed prefix of the conversation
  with a generated summary and stores that replacement as an immutable
  `compactions` row.

In both modes the original `messages` and `events` rows are never deleted.
`GET /v1/messages` always returns the real transcript, never a summary. A
later compaction only ever supersedes the previous one; superseded rows stay
in the database for inspection but no longer take part in prompt
reconstruction.

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
and sent live over the session's WebSocket connections, exactly like any other
message. If the agent reads a file containing a secret, or a command prints one
to stdout, that secret now exists in the SQLite transcript and in every client
receiving the session event stream. Peen does not scan for or redact
secret-shaped content in tool output. Treat the transcript and the event stream
at the same sensitivity level as the files and commands the agent can reach.

`remove_path` has exactly one built-in restriction, and it is a guard against
a catastrophic typo, not a permission system: it refuses to remove the
filesystem root or the current message's workspace directory itself. Every
other path, including everything named above, is removable.

Run Peen as a non-root user, in a container, with only the mounts, network
access, and capabilities the deployment actually needs. The production image
does exactly this by default; see below.

## Docker deployment

`make docker-build` builds the production image, tagged `peen`, from the
multi-stage `Dockerfile`. The final image is Ubuntu, not scratch or
distroless: `run_command` needs a real shell and the utilities an agent
reaches for (`git`, `curl`, `jq`, `ripgrep`, ...), so the runtime image is the
agent's toolbox, not just a binary carrier. It runs as a fixed non-root user,
UID and GID `10001`, under `tini` so `run_command`'s child processes get
reaped.

Peen keeps all durable state under `PEEN_CONFIG_DIR` and treats
`PEEN_WORKING_DIR` as the default workspace. Mount both from the host so a
container restart resumes existing sessions instead of starting over:

```bash
mkdir -p ./data/peen ./workspace
sudo chown 10001:10001 ./data/peen ./workspace

docker run --rm \
  --env-file .env \
  -p 8080:8080 \
  -v "$(pwd)/data/peen:/data/peen" \
  -v "$(pwd)/workspace:/workspace" \
  peen run
```

Both mounted directories must be owned by UID/GID `10001` before the
container starts. Peen enforces private permissions on `PEEN_CONFIG_DIR` and
`peen.db` on every startup, which requires owning them; a plain
`docker run -v` auto-creates a missing bind-mount source as root, which fails
that check. `PEEN_WORKING_DIR` must already exist: Peen changes into it
during startup and does not create it.

Keep `.env` out of version control. It already is: both `.env` and `.env.*`
are gitignored.

## Further reading

- [docs/peen/http-api.md](docs/peen/http-api.md): full request and response
  reference for every endpoint.
- [docs/peen/configuration.md](docs/peen/configuration.md): every `PEEN_`
  setting, the harness layering rules, and event and job/agent-run
  observability.
- [docs/peen/deployment.md](docs/peen/deployment.md): source build, `go
  install`, and the full Docker walkthrough.
- [docs/architecture.md](docs/architecture.md), [docs/development.md](docs/development.md),
  [docs/getting-started.md](docs/getting-started.md), and
  [docs/services-and-lifecycle.md](docs/services-and-lifecycle.md): the
  underlying Servicepack framework this project was cloned from.

---

*Built with spite using https://github.com/psyb0t/servicepack*
