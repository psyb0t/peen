# Changelog

All notable Peen changes per release. Versions follow
[semver](https://semver.org). Peen release history starts at v0.1.0. Entries
below document the Servicepack baseline from which Peen was created.

## v0.10.0 (2026-09-21)

An upstream now declares the wire protocol it speaks in `type`. The field was
called `provider`, which read as the vendor when the vendor is already the
upstream's `name`. A controller that cannot use its Docker socket also stays
up instead of refusing to start.

- Breaking: every `PEEN_UPSTREAMS` entry renames `provider` to `type`.
  `{"name":"aigate","provider":"openai",...}` becomes
  `{"name":"aigate","type":"openai",...}`. The accepted values do not change:
  `openai`, `anthropic`, and `zai-coding`. An entry left spelling it
  `provider` is refused at startup, because the unknown key is dropped and the
  missing type fails validation as `unsupported type ""`. Qualified model
  references such as `aigate/your-model-id` name the upstream and are
  unaffected.
- A controller that reaches a Docker socket but cannot build a launcher over it
  now refuses Docker profiles and keeps running, instead of failing to start.
  A controller run with numeric `--user uid:gid` against an image holding no
  account for that UID hit this, which stopped deployments whose sessions may
  never have named a Docker profile. Native profiles were unaffected then and
  still are. The controller logs the refusal once with
  `reason=docker_launcher_unavailable` and the socket path.
- The host identity failure names `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`,
  the two variables that resolve it. It previously reported only the numeric
  UID it could not look up, which said nothing about the fix.
- The production image installs its entrypoint without `COPY --chmod`, so it
  builds under the classic Docker builder as well as BuildKit. The installed
  mode and ownership are unchanged.
- A worker call whose answer fails to decode returns the zero value alongside
  its error rather than whatever the failed decode left behind.
- The API integration harness sets `PEEN_STATE_DIR`, which the control plane
  has required since v0.8.0. Without it the harness could not start its
  container, so that suite had been unable to run.
- The execution-form suite bounds one complete turn separately from startup.
  Sharing the startup budget made the suite fail whenever a turn outran the
  thirty-second read deadline, which reported a client timeout as a worker
  failure.

## v0.9.0 (2026-09-19)

Peen now installs in one command. Both routes build in a pinned Go image, so
neither needs a local Go toolchain.

- Adds `make install`, which places the built binary at `PREFIX`, defaulting to
  `~/bin`. It builds first, so it needs Docker and no local Go toolchain, and it
  warns when `PREFIX` is not on `PATH`. Pass `PREFIX` with `sudo` for a
  system-wide destination.
- Adds `install.sh` at the repository root for a one-shot install:
  `curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash`.
  It clones into a temporary directory, builds, installs, and removes the clone
  on the way out whether or not the build succeeded. `PREFIX` selects the
  destination and `REF` selects a tag or branch. It requires `git` and `docker`
  and stops when either is missing. Cloning and running `make install` by hand
  does the same thing.
- The README gains an Install section covering both routes, and
  [Deployment](docs/deployment.md) documents them alongside the existing source,
  `go install`, and Docker routes.
- The README contents list now includes the Docker deployment section the page
  already carried.
- Editorial pass over project comments and documentation prose. No behavior
  change.

## v0.8.0 (2026-09-19)

Peen is now a control service that starts with no sessions. A client names the
workspace it wants, and the returned session ID routes every turn.

- `peen run` now starts `control-core` and `control-api` instead of the single
  `http-server` service. `control-core` opens configuration, SQLite, the
  provider registry, the workspace policy, and the agent runtime, and reports
  ready only after those exist. `control-api` depends on it, serves REST, the
  global WebSocket feed, and metrics, and reports ready only once its endpoint
  answers a connection. Servicepack stops them in reverse order, so the API
  stops accepting work before SQLite closes.
- Adds local control-client commands: `peen control status`,
  `peen session open <workspace>`, and `peen session list`. They call the
  control API over the configured endpoint. A command that finds no controller
  takes a start lock under `PEEN_CONFIG_DIR`, re-checks while holding it, and
  starts `peen run` only if one is still absent, so two racing commands produce
  one controller. No command opens SQLite or starts a second supervisor.
  `peen control status` never starts a controller, so it can report that none
  is running.
- Adds `peen session attach <session-id>`, which streams one session's live
  events over the server-side session filter, and `peen session stop
  <session-id>`, which cancels the session's active turn and reports whether
  one was running.
- Splits the model loop out of the control plane. A session's turns now run in
  a worker process the controller starts on demand, and the controller keeps
  SQLite, REST, the WebSocket hub, routing, and worker lifecycle. A worker
  never opens the control database. It reaches durable state over a private
  per-session Unix socket carrying `run_turn`, `cancel`, and `shutdown`, and
  authenticates with a credential minted per worker generation. Only the
  credential's SHA-256 hash is stored. The raw value goes to the worker on
  stdin and never reaches a command line, an environment variable, a container
  label, or a log.
- Adds the internal `peen worker` command, which reads one launch document from
  stdin, connects back to the controller, and serves that session's turns. It
  is hidden from `--help` because the controller is its only caller.
- Every turn, post-start protocol event, job, and child-agent run records the
  worker generation that produced it in SQLite. The tool surface is the same
  `tools` package in every environment, so read-before-write hashes, no-replace
  creation, cancellation, and job signals are unchanged.
- Public WebSocket `message.send` now resolves the session's operator-selected
  profile, ensures that session's worker, and dispatches the turn to it. Global
  event fan-out happens only after the durable write, so a client never sees an
  event that a reconnect and replay would not produce.
- Adds operator-defined execution profiles through `PEEN_EXECUTION_PROFILES`
  and `PEEN_DEFAULT_EXECUTION_PROFILE`, with `native`, `docker.sandbox`, and
  `docker.host-like` as the documented names. A client names a profile and
  never sends an image, mount, network setting, or capability. An undefined
  name returns 403 and creates no session.
- Adds the native worker launcher, which starts the same installed Peen binary
  under its `worker` command, and the Docker worker launcher, which creates
  `peen-worker-<session-uuid>` from the configured image. A worker container
  carries exactly two labels, `peen.managed=true` and
  `peen.session=<session-uuid>`, is mounted at literal host paths with the
  configuration read-only, runs as the controller's host UID, GID, and
  username, and is refused if that identity is root or incomplete. The
  supervisor stops or removes a stored container ID only while both labels
  still match. Unit tests drive a fake Docker client; no live daemon coverage
  runs.
- Adds working `allowPrivilegeEscalation`. A Docker profile with it gives the
  worker account passwordless sudo inside its own container: the controller
  starts that container as root, and the image entrypoint creates the account
  for the controller's host UID, GID, and username, writes
  `/etc/sudoers.d/peen-worker` for that account alone, and drops to it with
  `setpriv` before the agent starts. The agent runs as the host user under every
  profile. Any other profile starts as that identity directly, never touches
  sudoers, and runs no root process. The image installs sudo and ships no
  sudoers rule, so sudo authorizes nobody until a profile asks. A `native`
  profile that sets the flag is refused at startup, because there is no
  entrypoint there to grant it.
- Docker authority is now a startup decision. The controller resolves its
  socket from `PEEN_DOCKER_SOCKET`, then a `unix://` `DOCKER_HOST`, then
  `/var/run/docker.sock`, and registers a Docker launcher only when one is
  reachable. Without it a session on a Docker profile is refused outright. Peen
  never falls back to a native worker, which would run tools on the host after
  the operator asked for a container.
- Adds `PEEN_WORKER_SOCKET_DIR`, `PEEN_DOCKER_SOCKET`, and `PEEN_WORKER_IMAGE`.
- Breaking: adds a required `PEEN_STATE_DIR`, and `PEEN_CONFIG_DIR` no longer
  holds durable state. `PEEN_CONFIG_DIR` is the global configuration and
  harness layer, and it is the one directory a Docker worker receives,
  read-only. `PEEN_STATE_DIR` holds SQLite, the audit logs, and the worker
  socket root, and no worker receives it. Peen refuses to start when the two
  are the same path or one sits inside the other. Before this, the
  configuration mount carried `peen.db` and the audit logs into every Docker
  worker, so one session could read every other session's transcript.
  `PEEN_WORKER_SOCKET_DIR` and `PEEN_LOG_DIRECTORY` now default under
  `PEEN_STATE_DIR`. Move an existing deployment's `peen.db` and `logs` into the
  new state directory and point both variables at their new homes. The check
  resolves both paths through their symlinks before comparing, so a state
  directory that is a link into the configuration directory is refused too, and
  a path that cannot be resolved refuses startup rather than falling back to
  the literal string.
- A worker now receives only its own session's socket directory,
  `<root>/<session-uuid>/worker.sock`, instead of the shared socket root. The
  root lists every live session's socket, so mounting it showed one worker
  where every other session's controller surface lived.
- The worker generation now records the repository digest the daemon reports
  for the image, so naming a tag still leaves a durable record of the bytes that
  ran. It previously stored the local image ID from a container inspect and
  called that a digest, which no registry can resolve. An image the daemon holds
  no repository digest for, such as one built locally and never pushed, records
  no digest rather than failing the launch.
- Adds the opt-in `make test-docker-worker` target, the image-entrypoint
  contract test. It creates one test-owned container from a named image inside
  a disposable root under the repository's gitignored `.testing/` directory,
  builds the request through the production launch builder, then replaces the
  worker command with a fixed probe that records what the dropped process can
  see. It asserts the effective UID, GID, and username, that `sudo -n true`
  fails, that no Docker socket is present, that the network is unreachable,
  that the configuration mount is readable while a file planted under the state
  directory is not, that another session's socket is invisible while this
  session's own directory is not, and that a file the probe writes carries host
  ownership. Teardown inspects the exact recorded container ID and acts only
  while the ownership labels still match. It runs no agent, no model, and no
  controller database, and it is not part of `make test`.
- Adds `GET /v1/execution-profiles`, reporting each profile with
  `hostRootEquivalent` and a `capabilityWarning`, and
  `GET /v1/session/workers`, reporting one session's durable worker
  generations.
- A Docker worker now runs the image published alongside the controller running
  it. Peen takes `PEEN_WORKER_IMAGE` if it is set, then the profile's own
  `image`, then `psyb0t/peen` tagged with the controller's own build version, so
  a release runs the worker built beside it without anyone editing
  configuration. A build with no release tag reports `dev` and names an image no
  registry holds; set `PEEN_WORKER_IMAGE` to run a Docker worker from a local
  build. A Docker profile no longer has to carry an `image` of its own.
- Removes the worker image rules. A reference previously had to be exactly
  `psyb0t/peen@sha256:<digest>`: no tag, no other repository. No release could
  satisfy that for itself, because the digest does not exist until the push that
  creates it finishes, so every deployment had to read one off the registry by
  hand afterwards. It also blocked a locally built image, a mirror, and a fork,
  while stopping nothing: anyone who can set `PEEN_WORKER_IMAGE` already holds
  the controller's Docker socket and can run any container without asking Peen.
  Any reference the daemon can resolve is now accepted. A worker container still
  starts as root so the Peen entrypoint can drop it to the host account, so an
  image without that entrypoint fails when the worker runs.
- The Docker worker launcher now pulls an image the daemon does not already
  hold. The Engine API's container create does not pull, unlike the docker CLI,
  which pulls on a 404 and retries, so a worker could not start on a host that
  had never seen the image.
- Fixes `POST /v1/session/jobs/{jobId}/signal`, which recorded every signal as
  unaccepted while the command kept running. A job's process group is a child of
  the session's worker, and the endpoint read the controller's own job registry,
  which is empty after the worker split. The signal now travels to the worker
  over a new `signal_job` command, and the worker's own signal path both reaches
  the process group and records the durable request. A session with no live
  worker still records the request against the durable row and reports that
  nothing was signalled.
- Fixes `POST /v1/session/cancel`, which answered `cancelRequested` and then let
  the turn run to completion. The endpoint reached the controller's own store,
  which records the request and fires the cancellation registered in this
  process. After the worker split the turn's cancellation is registered in the
  worker, so the controller held nothing to fire and the model loop never
  stopped. The endpoint now also routes to the session's worker, which is where
  `peen session stop` ends up too. `control.TurnRouter.CancelSessionTurn`
  already did the routing and had no callers.
- Worker log records now reach the controller's sinks. A worker runs the model
  loop in its own process, so its records for hook actions, skill activation,
  child-agent lifecycle, and tool outcomes were written there and never passed
  through the controller's logging stack. The daily audit file kept controller
  records only, against the trail `docs/configuration.md` describes. A native
  worker's output is read from a pipe instead of inherited file descriptors, a
  Docker worker's is followed from the container, and both are re-emitted at the
  worker's own level with its session and generation attached. A worker still
  receives neither the audit directory nor the state directory.
- Fixes `GET /v1/session/context-snapshots/{contextHash}`, which returned 500
  for every snapshot Peen has written since the endpoint shipped in v0.4.0. The
  turn writer stores the harness manifest as the JSON array it is, one entry per
  contributing layer, while the read path decoded it into an object and the
  schema declared one. The write path only checks `json.Valid`, which an array
  satisfies, so snapshots stored cleanly and every read failed. `manifest` is
  now an array of `ContextManifestEntry` carrying `kind`, `name`, `source`,
  `priority`, and `hash`. This changes a response type inside `/v1` rather than
  opening a new major, because no client can depend on a shape the endpoint
  never returned, and because it makes the snapshots already stored in an
  existing database readable.
- `make test-real` now checks what a live turn writes to SQLite. The lifecycle
  test opens a session, routes the turn through the session metadata, and then
  reads back one native worker generation in `ready` with no container or image
  digest, one completed turn, a durable event stream whose sequence never
  repeats or skips and whose events name only known turns and that one
  generation, and completed model runs whose per-round provider calls carry
  real token counts. The wake turn must reuse the same generation.
- Adds a live one-session, many-clients and queueing test. Two WebSocket
  clients attach to one session, and the client that sent nothing still
  receives the other's live events and its completion, each frame naming the
  session. A message sent while that turn is running is answered with `queued`,
  joins the running turn's transcript, and leaves the session with one turn
  instead of two.
- Adds a live cancellation test, which stops a turn already several tool rounds
  into a real model loop, then checks the WebSocket failure code and that the
  turn is recorded as cancelled rather than failed.
- The real suite renews its WebSocket read deadline per frame instead of
  spanning the whole turn. A long turn that keeps emitting events no longer
  fails at a fixed wall-clock limit, and a silent one still does.
- Shutdown now stops every session worker after supervised jobs and before
  SQLite closes, and records each stop, so no worker outlives the process with
  a durable row still claiming it is ready.
- Adds `POST /v1/session/reconfigure`, which moves an idle session to another
  operator-defined profile. It takes a profile name and a reason and nothing
  else, refuses an undefined profile with 403, refuses a session with a turn in
  flight with 409, and stops the session's worker so the next turn starts a new
  generation under the new profile. Every change is stored in
  `session_profile_decisions` and readable through
  `GET /v1/session/profile-decisions`.
- The `psyb0t/peen` image now defaults to `run`, so `docker run psyb0t/peen`
  starts a control plane. The same image is the worker image: the controller
  creates a container from it with the `worker` command.

- Breaking: starting Peen no longer opens a session. Open one with
  `POST /v1/sessions/open`, which resolves a workspace path to its durable
  session and creates that session the first time the directory is opened.
  Opening the same directory again, by any of its names, resumes it.
- Breaking: WebSocket `message.send` now takes the session in the event's
  `sessionId` metadata, replacing the rule that refused a client-selected
  session. Naming a session routes the message, it does not authorize it: Peen
  loads the session before starting a turn, so an unknown ID writes no turn
  record. A deployment with no session and no named session refuses the
  message.
- Adds `GET /v1/sessions`, which lists durable sessions and takes no session
  header because it is how a client discovers them.
- Adds `PEEN_WORKSPACE_ROOTS`, a JSON array of absolute paths a client may open
  as a workspace. An unset value allows only the process working directory. A
  path outside every root returns `403 WORKSPACE_NOT_ALLOWED` and creates no
  session. Peen resolves a requested path through its symlinks before the root
  check, so an alias of an allowed directory opens the same session and a
  symlink escaping a root is refused.
- Breaking: `pkg/peen` no longer names a session anywhere in its contract.
  `MessageResult.SessionID`, `ListMessagesRequest.SessionID`, and
  `SessionDetails.ID` are gone, `Session(ctx, id)` becomes `Details(ctx)`, and
  `Cancel(ctx, id)` becomes `Cancel(ctx)`. A direct runtime is one workspace and
  owns its durable session privately. Session IDs exist so a controller can
  route between workspaces, which is not a problem an embedding caller has.
- The binary now calls itself `peen` in its own `--help` and in the `binary`
  field of every log line, whatever built it. It previously reported the
  framework's name unless the build passed `-ldflags -X main.appName=peen`, so
  `go install` and a plain `go build` produced a binary that introduced itself
  as Servicepack.

## v0.7.0 (2026-09-13)

Peen now binds each running process to one durable session for its startup
workspace. Restarting with the same SQLite state and working directory resumes
that session.

- Breaking: `PEEN_WORKING_DIR` is removed. Start Peen from the workspace you
  want it to operate in. Docker runs must use `--workdir /workspace` with the
  project mounted there.
- Breaking: WebSocket `message.send` frames no longer accept a client-selected
  session or workspace. The server reports its startup session ID in event
  metadata. Use it with the REST read and control endpoints.
- Sessions now record their canonical workspace path. The REST session payload
  includes that path so clients can identify the workspace that owns a trace.

## v0.6.0 (2026-09-13)

Peen now ships a dedicated agent package for configuring and operating Peen.

- Replaces the inherited Servicepack agent metadata with the `peen` skill for
  Claude, Codex, and OpenClaw. The skill covers provider configuration, Docker
  deployment, WebSocket turns, durable session control, and workspace harness
  layers.
- Adds marketplace installation guidance to the README. The central marketplace
  entry follows this release.
- Includes the embedded base `AGENTS.md` in clean checkouts. Lint now rejects a
  Go embedded asset that Git ignores, preventing a local build from masking a
  CI compile failure.

## v0.5.1 (2026-09-13)

Fixes the release checks after v0.5.0.

- Execution-form tests now put the session ID in `message.send` metadata,
  matching the global WebSocket contract.
- Peen's test targets and scripts allow thirty minutes per Go test package.
  Production-image setup can use twenty-five minutes for a cold CI build.
- Docker build contexts exclude local plan and coverage directories.

## v0.5.0 (2026-09-13)

WebSocket delivery is now global by default. A client can build and switch
conversation tabs from live event metadata while one connection remains open.

- Breaking: connect to `GET /v1/ws` and put the client-generated canonical
  session UUID in `metadata.sessionId` on every `message.send`. The old
  `?sessionId=` parameter no longer selects the session for a message. It is
  now an optional outbound filter for one connection.
- Global sockets receive accepted events for every session. Clients that want
  only one conversation may use the optional `?sessionId=` filter or filter
  events locally by `metadata.sessionId`.
- Fixes durable sequence allocation after database error mapping, so a
  session's first notice, job-output line, and child-agent event persist and
  replay correctly.
- Updates the README and guides with the complete WebSocket frame, global-feed
  model, durable model-call records, and known-session REST reads.

## v0.4.1 (2026-09-12)

Fixes the release pipeline, which failed before any job ran. The pipeline now
starts, so a tag push builds and publishes the Docker image, creates the GitHub
release, and publishes the skill to ClawHub.

- Grants the `publish-to-clawhub` job `contents: read`. Under the top-level
  `permissions: {}`, that job inherited no permissions, so the ClawHub reusable
  workflow could not be granted the `contents: read` its jobs need and GitHub
  rejected the whole workflow file at parse time.

## v0.4.0 (2026-09-12)

Peen now records the complete session trace in SQLite and exposes it through
the REST API. A reconnecting client can inspect the same work, including what
providers, hooks, tools, jobs, compactions, and child agents did.

- Breaking: outside producers now post notices to
  `POST /v1/session/notices`. `GET /v1/session/events` is the durable protocol
  transcript. Clients that followed a child agent now use
  `GET /v1/session/agents/{agentRunId}/events`.
- Adds durable child-agent runs, events, final results, independent
  cancellation, process jobs and output, notices, prompt snapshots, and
  context snapshots.
- Stores complete model-run and provider-round records, including model and
  connection identity, non-secret settings, messages, thinking, tool
  definitions, retries, token categories, usage, costs, timing, and failures.
- Adds compaction replay. A message keeps its direct `compactionId` forever.
  Later summaries point to the earlier compaction, preserving the full
  lineage without overwriting older links.
- Removes the JSONL agent transcript mirror. SQLite is the only durable replay
  store.

## v0.3.2 (2026-09-11)

Fixes CI delivery and makes the production integration harness wait for the
actual ready endpoint.

- Publishes multi-architecture Docker images after code checks pass, then
  publishes the ClawHub skill after the image build.
- Removes race instrumentation from Peen test targets and sets the project
  coverage floor to 80 percent, matching the full suite's measured coverage.
- Waits for the production container's `/ready` endpoint instead of a startup
  log line, and gives failed test setup its own bounded cleanup context.
- Links the user docs to the upstream libraries that provide provider drivers,
  streaming, HTTP and WebSocket transport, lifecycle, configuration, logging,
  storage, and metrics.

## v0.3.1 (2026-09-11)

Documentation now describes Peen directly instead of the framework it started
from.

- Moves the user guides to `docs/` and removes the redundant `docs/peen/`
  directory.
- Replaces the old Servicepack onboarding, architecture, lifecycle, and update
  material with Peen setup, architecture, and development guidance.
- Keeps Servicepack as a short upstream reference for its process lifecycle
  plumbing.

## v0.3.0 (2026-09-11)

WebSocket is now Peen's only agent-turn transport. REST remains for durable
session reads and control operations.

- Breaking: Removed `POST /v1/messages` and HTTP Server-Sent Events. Send
  `message.send` over `GET /v1/ws?sessionId=<uuid>` instead. A session UUID is
  created atomically from the first accepted message, and all connections for
  that session receive the same native agent events.
- REST never creates or submits agent turns. It still lists durable messages,
  session state, events, jobs, and child-agent runs, and provides cancellation
  and external session-event control.
- Updated aichteeteapee to v1.14.0 and regenerated the vendored dependency
  tree.

## v0.2.0 (2026-09-10)

Adds a synchronized WebSocket session interface and moves the HTTP server to
Serbewr.

- Adds `GET /v1/ws?sessionId=<uuid>` for synchronized session clients. Browser
  clients authenticate with a WebSocket subprotocol, and connections in the
  same session receive the same agent events.
- Replaces Echo with aichteeteapee Serbewr and WShub while retaining the
  existing JSON, Server-Sent Events, session, event, job, and child-agent API
  contracts.
- Updates the API runtime and vendored dependencies, removing the unused Echo
  stack.

## v0.1.0 (2026-09-10)

Initial release of Peen, a stateful coding-agent service.

- Exposes durable agent turns, queued user messages, cancellation, job control,
  child agents, and both JSON and Server-Sent Events over the HTTP API.
- Resolves workspace instructions, Agent Skills, named agents, events, and
  additive lifecycle hooks from the configuration and workspace layers.
- Provides safe file tools that require a current-turn content read before a
  mutation and publish new files without replacing existing paths.
- Supports OpenAI-compatible, Anthropic-compatible, and Z.ai Coding providers,
  including thinking streams across tool rounds.
- Adds structured audit logs, a loopback metrics listener, built-in planning
  and freshness skills, and live end-to-end harness coverage.
- Updates runtime and test dependencies to patched releases, including Kin
  OpenAPI, Echo, Moby archive, x/crypto, x/mod, Elelem, and Essesey.

## v1.9.2 (2026-08-21)

Removes `make audit`; `make sec` is the single security-scan entry point.

- `make audit` only ran govulncheck, and `make sec` already runs it (govulncheck
  + semgrep, merged into `sec.sarif`). Keeping both was a redundant second way to
  run the same scanner, so `make audit` is gone. Run `make sec` instead. CI
  already used `make sec`, so the security gate is unchanged.

## v1.9.1 (2026-08-21)

Hardens `make servicepack-update` against two ways it could break a downstream.

- The pre-update backup now archives only what git tracks plus untracked files
  that are not gitignored, instead of a blind `tar .` of the whole tree. The old
  backup swept in gitignored local state (build caches, a dev stack's runtime
  dirs, a privileged container's root-owned files) and failed outright when any
  of it was unreadable, cancelling the update on exactly the machines that run a
  dev stack. The new backup captures the same set the update can affect and never
  chokes on regenerable local state.
- The update now fails before touching anything if the downstream's `.gitignore`
  hides a framework file the sync would deliver. rsync ignores `.gitignore` and
  writes the file, but the update's `git add -A` honors it and silently skips it,
  so the file would build locally yet never land in the commit, breaking CI and
  fresh clones. A dry run now checks the would-sync set with `git check-ignore`
  and stops with the offending paths.

## v1.9.0 (2026-08-21)

Framework updates now always protect a downstream's `docs/` and `tests/` trees.

- `make servicepack-update` adds `docs/` and `tests/` to the rsync's mechanical
  exclude floor, so an update can never overwrite a project's own docs or its
  test tree (the testcontainers harness plus its service tests). Previously only
  `tests/` was protected, and only as an optional line each project had to copy
  into its `.servicepackupdateignore` by hand. A project that had not copied it
  would have its `tests/testinfra` overwritten by the framework's baseline.
- Removed the now-redundant `tests/` opt-out from the shipped
  `.servicepackupdateignore` template. Pulling the framework's baseline test
  harness on update is no longer offered, because the floor always protects
  `tests/`.
- Updated the ownership docs (README layout, getting-started, framework-updates,
  development, and the agent skill) to state that `docs/` and `tests/` are yours
  and are never touched by an update.

## v1.8.1 (2026-08-20)

Fix: anchor the `build` gitignore so the vendored dependency tree is complete.

- The `build` ignore pattern (for the `build/` output directory) was unanchored,
  so it also matched `vendor/github.com/moby/moby/api/types/build/` and dropped
  that package from git. testcontainers-go pulls that package in, so the tree
  built locally but CI, which checks out only what git tracks, failed with
  `cannot find module providing package github.com/moby/moby/api/types/build`.
  Anchored it to `/build/`.

## v1.8.0 (2026-08-20)

Make the framework testcontainers-ready and ship an extendable integration-test
harness.

- `DEV_RUN_DIND` now runs on the host network, so a test process inside the dev
  image can reach testcontainers' host-published ports at `localhost:<mapped>`.
  Integration and coverage runs that start real sibling containers now work
  without each project patching the runner. It applies to dev/CI test runs only,
  never production.
- New `tests/testinfra` harness and a `tests/integration` boot smoke that builds
  the servicepack image from the repo `Dockerfile` and runs it, confirming the
  app boots with whatever services are registered. Both are a starting point:
  extend `Infra` with the real dependencies your services need (a database, a
  cache, a broker via testcontainers-go) and add tests that drive them.
- `tests/` is now in `.servicepackupdateignore`, so a framework update never
  overwrites your integration tests once you have extended them.
- Added `testcontainers-go` as a test-only dependency for the harness. A
  downstream that imports none of it drops it on the next `make dep` tidy.
- Documented the harness in `docs/development.md`.

## v1.7.2 (2026-08-20)

Fix: `make servicepack-update` re-formats the framework files it rewrites, so a
renamed import path stays correctly sorted and passes the downstream's `make lint`.

- The update rewrites the framework module path into each synced `.go` file with
  a plain string substitution, which renames imports but leaves their order
  alone. gci/gofumpt sort imports alphabetically on the full path, so swapping
  `github.com/psyb0t/peen/...` for a downstream module can move where
  those imports sort and leave a file such as `cmd/main.go` mis-ordered. The
  update now re-runs the project formatters over exactly the rewritten files.
- Added an internal `make servicepack-reformat` target that formats a given file
  list (gci/gofumpt/goimports via `golangci-lint fmt`); the update calls it after
  `make dep`.

## v1.7.1 (2026-08-20)

Docs: list `make sec` in the development command reference.

## v1.7.0 (2026-08-20)

New `make sec` security scan and a migration to the `code-workflow` reusable flow.

- `make sec` runs govulncheck (via `go tool`) and semgrep, merging both into
  `sec.sarif` for the GitHub Security tab and failing on any finding. The
  development image now ships semgrep.
- The CI pipeline moves off `go-workflow` onto `code-workflow` (checks) plus
  `release-workflow` (the tag release), chaining `make lint` / `make
  test-coverage` / `make sec` / `make generate`, and uploads the `sec` SARIF to
  the Security tab.
- Suppressed a benign semgrep finding in the example-flaky service, which writes
  a restart-surviving attempt counter to a fixed path on purpose.

Downstream projects get `make sec` and the semgrep-equipped dev image on the next
`make servicepack-update`; their own pipeline keeps running until they migrate it.

## v1.6.4 — 2026-08-17

The GitHub Actions caller now requests Go 1.26.6 exactly. Previously the
minor-only `1.26` selector resolved to the runner's Go 1.26.5 cache, so the
security scan could not load this module after its Go 1.26.6 requirement.

## v1.6.3 — 2026-08-17

Docker-backed Testcontainers targets now pass the socket path explicitly and
disable Ryuk inside the test container. The test suite still owns explicit
teardown; this avoids Ryuk's unreachable callback path when `make` runs from a
container while keeping `make test-coverage` portable across host, container,
and GitHub Actions runners. Projects can append narrowly scoped Docker run
arguments through `DEV_RUN_DIND_EXTRA_ARGS` instead of copying the runner.

## v1.6.2 — 2026-08-17

Builds now use Go 1.26.6 everywhere: development and production Dockerfiles,
the framework Dockerfiles copied to downstream projects, and `make build`.
This fixes projects that require Go 1.26.6 failing because the framework build
container was still pinned to Go 1.26.4.

## v1.6.1 — 2026-08-17

Lint fix only, no behavior change.

- Reflowed the `-ldflags` build-example comment in `cmd/main.go` to satisfy the
  golangci-lint `lll` 80-column limit (the single-line example was 106 chars),
  keeping the command copyable as a tabbed godoc code block.

## v1.6.0 — 2026-08-16

Every Servicepack binary now carries a build version alongside its existing
binary name and commit identity. Release builds use the exact tag at `HEAD`;
untagged source builds use `dev`. The three values are available in the global
log scope as `binary`, `commit`, and `version`, and the framework Docker build
paths pass all three linker flags consistently.

## v1.5.0 — 2026-08-15

The coverage gate now covers **services**, so a service-heavy project no longer
has to override `test_coverage.sh` just to gate its own code. Previously the
script excluded every `internal/pkg/services/` package (and did no cross-package
or integration coverage), which meant the bulk of a real app went unmeasured
unless the downstream reimplemented the script.

`test_coverage.sh` now:

- instruments **every** module package (`-coverpkg=<module>/...`) and runs with
  `-tags=integration`, so an integration test under `tests/` credits coverage to
  the production package it drives — nothing is hand-selected;
- merges native **covdata** from a service that runs out-of-process in a real
  container. The script exports `SERVICEPACK_COVDATA_DIR`; a project's
  integration test mounts it into that container as `GOCOVERDIR`, and the merge
  folds it into the total (union of the highest hit count per block);
- excludes from the floor only what is not a project's hand-written code under
  test: `cmd/` mains, the `tests/` harness, generated `*.gen.go`, the framework
  service-manager mocks, and the framework's own `example-*` / `hello-world`
  demo services (which every real project deletes).

Downstreams pick this up with `make servicepack-update`. A project whose
services were not previously gated may need to add tests to clear the floor.

## v1.4.0 — 2026-08-14

Framework updates now fail fast on missing tools instead of stranding the repo
on a half-made update branch, and the coverage gate no longer counts generated
code — so a downstream that commits generated servers/repositories doesn't have
to override the coverage script just to exclude them.

- `servicepack-update` now preflights every external tool the update shells out
  to — `git`, `tar`, `rsync`, `jq`, `docker`, `go` — BEFORE creating the backup
  and update branch, and reports every missing one at once. Previously a missing
  `rsync` / `jq` surfaced only mid-update, after the branch had already been
  created, leaving a dirty tree to clean up by hand. A shared `require_cmds`
  helper in `common.sh` makes the same preflight available to any script.
- `make test-coverage` now also filters `*.gen.go` out of the coverage profile
  (alongside the framework's service-manager mocks). Generated code is not
  hand-written and must not count toward — or dilute — the coverage floor, so a
  downstream committing generated servers/repositories no longer needs to
  override the coverage script. Lint already skips generated files via the
  golangci-lint `generated: lax` setting.

## v1.3.4 — 2026-08-14

The `make build` / `make run-dev` build target no longer breaks in a project
that adds a second `cmd/` main (a codegen tool such as `cmd/repogen`).

- Build `./cmd` instead of `./cmd/...` in `build.sh`, `run_dev.sh`, `Dockerfile`
  and `Dockerfile.servicepack`. `./cmd/...` compiled every `main` under `cmd/`
  into a single `-o` output, which fails ("cannot write multiple packages to a
  non-directory") the moment a downstream drops a tool binary next to the app
  entrypoint. `./cmd` builds only the app package in `cmd/`; a repo-local tool
  belongs in the `go.mod` `tool` block and is run via `go tool`, never built
  into the app image.

## v1.3.3 — 2026-08-14

Dependency maintenance now works correctly in repositories that commit their
Go vendor tree, and the bundled psyb0t libraries and tools are current.

- Make `pkg-add`, `pkg-update`, `pkg-upgrade`, and `pkg-add-tool` resolve
  modules with `-mod=mod` before regenerating `vendor/`, instead of letting Go
  force those mutation commands into read-only vendor mode.
- Updated `ctxerrors` to 0.7.1, `goenv` to 1.0.10, `gonfiguration` to 1.6.4,
  `slogging` to 1.7.1, and the `gofindimpl` tool to 1.0.12.

## v1.3.2 — 2026-08-14

Fresh clones can complete `make own` again, and the split test targets keep
their intended Docker boundaries.

- Regenerate `internal/pkg/services/services.gen.go` before dependency tidying,
  so deleted example-service imports cannot make `make own` fail before it
  initializes the new Git repository.
- Give Dockerized Go commands an explicit writable `GOPATH`, and keep
  `make test-unit` on the socketless unit-test path instead of routing it
  through the Docker-enabled full test target.

## v1.3.1 — 2026-08-14

Documentation and agent guidance now explain the framework without burying the
useful shit in the root README.

- Rebuilt the documentation hierarchy: the concise README points to explicit
  guides for getting started, service lifecycle, development, architecture, and
  framework updates, with technical READMEs beside the service manager, runner,
  and framework Make scripts.
- Clarified the actual deployment choice: compose related services locally for
  easier debugging, then ship one binary or split them into independent
  microservices when their scaling, ownership, or failure boundaries demand it.
- Corrected agent setup to use the Docker-backed Make targets and removed the
  false claim that `make own` rejects an older host Go toolchain.

## v1.3.0 — 2026-08-14

Scoped logs and a container-only development workflow, without breaking the
existing service API.

- Added `ctxscope` throughout the runner, application, service manager, bundled
  examples, and generated-service template. Logs now carry the binary and build
  commit globally, plus the current service where work runs.
- Added `runner.RunContext(ctx, runnable)` for callers that need to preserve a
  parent context. The existing `runner.Run(runnable)` keeps its background-context
  behavior for compatibility.
- `make test`, `make test-integration`, and `make test-coverage` execute in a
  Docker dev container with the Docker socket available for Testcontainers;
  coverage now enforces a 90% minimum. Shell formatting and ShellCheck run in
  that same dev image.
- Updated the pinned Go toolchain and build images to Go 1.26.4, including the
  agent setup reference and generated-service documentation.

## v1.2.23 — 2026-08-08

Documentation. No code change.

- The scaffolded-service snippet showed a body that waits on `ctx.Done()` and
  returns nil. `scripts/make/servicepack/service.sh` emits
  `panic("TODO: Implement <name> service logic")` — the opposite of a service
  that quietly does nothing, and deliberately so.
- The registration step named `scripts/make/service_registration.sh`; the script
  is at `scripts/make/servicepack/service_registration.sh`.
- The example-services list omitted `example-nested/http` and
  `example-nested/grpc`, which ship.
- The coverage note said examples and `cmd` are excluded. The exclusion is
  `/cmd` plus the **entire** `internal/pkg/services` tree — every user service,
  not just the examples — and `service-manager/mocks.go` is filtered out of the
  profile afterwards.
- Clarified that `make own` removes the `example-*` services and keeps
  `hello-world`, which `own.sh` says in as many words.

## v1.2.22 — 2026-08-08

`slogging` v1.7.0. The framework's own code was unaffected; the scaffold's docs
were not.

- `slogging` v1.6.1 → v1.7.0, which rebuilt that module's handler API:
  `MultiWriterHandler` is gone, the fan-out moved to a `handlers` package, and
  `AddHandler` split into **`AddSink`** (add a destination) and **`SetOutput`**
  (change where the process prints, keeping the destinations). The framework
  uses the blank import and never names a handler type, so no framework code
  changed and the vendor tree just follows.
- **The custom-handler example is now `slogconf.AddSink(myCustomHandler)`** — in
  the README, and in `cmd/init.go`'s own comment. That is the part that actually
  mattered here: a project generated from this scaffold copies those, so an
  example calling a function that no longer exists would fail to compile in
  every new project while the framework itself built fine.
- If you have a generated project on the old call, `AddHandler` becomes
  `AddSink`. Reach for `SetOutput` instead when what you wanted was to replace
  stdout/stderr rather than add alongside it — adding a second console handler
  prints every line twice.

## v1.2.21 — 2026-08-08

The logging dependency was renamed upstream. No framework behaviour changed.

- `github.com/psyb0t/slog-configurator` is now `github.com/psyb0t/slogging`, with
  the configurator at `slogging/slogconf`. The blank import in `cmd/main.go` and
  the vendor tree follow it. Every exported name is unchanged, so `AddHandler`,
  `SetHandlers` and the `LOG_LEVEL` / `LOG_FORMAT` / `LOG_ADD_SOURCE` variables
  behave exactly as before.
- The docs that tell you what to import moved with it: the `cmd/init.go`
  custom-handler example in the README and in `cmd/init.go`'s own comment, the
  dependency list, the env-var section, and the skill under `.agents/`. These
  matter more than the import itself here — scaffolded projects copy them.

## v1.2.20 — 2026-08-06

Two concurrency bugs in the core, a broken quick start, and a command-injection
hole in the updater. The two concurrency fixes are the reason to take this one.

### Fixed

- **A shutdown racing a service error killed the process.** `App.Run` registered
  its deferred `close(errCh)`, `wg.Wait` and `Stop` in an order that, because
  defers run LIFO, executed them backwards: the error channel was closed while
  the goroutine that sends on it was still live. A stop signal arriving while
  `ServiceManager.Run` was unwinding toward a non-nil error therefore panicked
  the whole process with `send on closed channel` — during what is supposed to
  be a graceful shutdown, and nothing recovers it. `ServiceManager.Run` already
  registered the same three in the correct order; `App.Run` disagreed with it.

- **Three or more services failing at once hung the process forever.** The
  manager's error channel has capacity 1 and `Run` receives from it exactly
  once, but `handleServiceError` used a plain blocking send. The second and
  later concurrent non-allowed failures parked on that send permanently, so
  `Run`'s deferred `wg.Wait` never returned: no error, no exit, just a hang.
  A bare send is not selectable, so context cancellation could not break it out
  either. The send is now non-blocking and the dropped errors are logged. That
  matches the documented contract — the *first* non-allowed failure stops
  everything and the rest are consequences of that same shutdown.

- **`make build` built nothing.** The root `Makefile` shipped a live `build:`
  target that only echoed, shadowing the framework's real one — so the quick
  start in this README (`make own` → `make build` → `./build/<name> run`) failed
  at the third step for everyone who followed it. The example override now ships
  commented out.

- **`make servicepack-update` could execute arbitrary commands from
  `.servicepackupdateignore`.** The rsync exclude list was built by string
  concatenation and run through `eval`, so a shell metacharacter in that file —
  a backtick, a `$(...)`, a `;` — ran during the update. It is now a bash array
  passed directly to rsync, with no `eval`; entries can only ever be patterns.
  Scope worth stating plainly: that file is excluded from the sync, so an
  upstream release could never inject into a downstream tree. This was a local
  vector, not remote code execution.

- **Base images were unpinned, and the runtime image floated on `alpine:latest`.**
  Tags are mutable: an upstream can republish different bytes under the same name
  and the next build consumes them silently. All four are now pinned by digest —
  including the `docker run` inside `build.sh`, which is what `make build`
  actually uses. Pinning only the Dockerfiles would have left the default build
  path unprotected.

- **The production image's binary introduced itself by the wrong name.** The
  Dockerfiles never injected `-X main.appName`, so the binary fell back to the
  literal `servicepack` in its own `--help` regardless of the project's module
  path. `make build` had always injected it; the image build had not.

- **Script diagnostics went to stdout.** Every helper in `common.sh` wrote its
  banners to stdout, and all framework scripts source it, so anything capturing
  a script's output got the decoration mixed into the data. They now write to
  stderr.

- **Three error sites returned bare errors**, contradicting this README's own
  claim that all errors carry `ctxerrors` context. Note for anyone doing the
  same sweep: one of those values can legitimately be nil, and wrapping a nil
  emits a spurious error log rather than passing it through — the wrap belongs
  inside the non-nil branch.

- **`internal/app` had a test that synchronized on a fixed sleep**, asserting
  services were running after 20ms. It failed under load and reported a
  service-startup bug that did not exist. It now waits for the condition.

### Notes

- Both concurrency fixes ship with regression tests that were verified by
  reintroducing the original code: the old defer order produces
  `panic: send on closed channel`, and the old blocking send produces a hang at
  three and ten concurrent failures. Two services alone does *not* hang, because
  the single receive drains the buffer just in time — which is why this survived
  as long as it did.
- The injection fix was likewise verified in both directions: the old form
  executed a `$(...)` planted in the ignore file; the new one treats it as a
  literal pattern.
- `Makefile` is excluded from the update sync, so an existing project keeps its
  own copy — comment out the `build:` override by hand if yours still has it.

## v1.2.19 — 2026-08-06

Build-context and ignore-file hygiene. No code change.

### Fixed

- **`.research_files` was never gitignored.** The entry read `research_files`,
  without the leading dot, so it matched nothing and the directory it was meant
  to cover was tracked-eligible the whole time. Nothing had been committed. The
  dotted form is now present; the bare one is left in place in case a project
  uses it.

- **`.dockerignore` was a single line (`.telemetry/`), and every Dockerfile does
  `COPY . .`.** The build context therefore carried the entire working tree, and
  because `Dockerfile.dev` / `Dockerfile.servicepack.dev` end at that `COPY`,
  whatever it picked up shipped *inside* the dev images — including
  `git-update.sh`, the gitignored local release script. It now excludes git and
  CI metadata, root-level and `docs/` documentation, env files, local tooling,
  build output, coverage, logs, editor state, backups and research scratch.

### Notes

- **`vendor/`, `*_test.go` and nested markdown are deliberately kept.** The
  build runs with `-mod=vendor`, the dev image runs `make test` against the
  copied source, and a package may `go:embed` markdown as a resource — a blanket
  `**/*.md` would break that silently and confusingly. Markdown is dropped at
  the repository root and under `docs/` only; `*.md` does not cross a `/`, so it
  never reaches nested files. The reasoning is written into the file so it
  survives a future tidy-up.
- `.agents` is excluded too: it is published from the repository itself and is
  never built into or read by the binary.
- Verified by building both images and listing the result: `git-update.sh`,
  `.git`, root markdown and `.agents` absent; `vendor/`, `cmd/` and the test
  files present.
- `.gitignore` is excluded from the update sync and `.dockerignore` ships as a
  default opt-out, so **existing projects do not receive either change** — copy
  the entries across by hand. New projects get them at scaffold time.

## v1.2.18 — 2026-08-06

The update's module-path rewrite no longer walks your whole working tree.

### Fixed

- **`make servicepack-update` rewrote Go files it had never delivered, anywhere
  under your repo.** After syncing the framework, the update rewrites
  servicepack's import path to your module path. That rewrite ran as
  `find . -type f -name "*.go" -not -path "./vendor/*" -exec sed -i ...`, and
  `find` does not honour `.gitignore` — so it descended into scratch
  directories, nested clones and any other ignored tree. In a real project it
  visited 62,026 files instead of the ~50 the sync had delivered, and rewrote
  the imports of an unrelated servicepack checkout that happened to live under
  the repo. Nothing showed up in `git status`, because everything it damaged was
  ignored.

  The rewrite is now scoped to rsync's own transfer manifest, captured while the
  sync runs. That manifest is the exact set of files the update delivered, with
  every exclude and every `.servicepackupdateignore` entry already applied — so
  a file the update did not touch can no longer be edited by it.

- **The companion `-name "*.mod"` walk is gone.** It had no legitimate target:
  rsync never copies `go.mod`, its module line is rewritten directly, its
  requires are merged upgrade-only by `merge_framework_deps`, and `make dep`
  tidies and vendors. Its only reachable effect was rewriting `go.mod` files in
  directories the update had no business entering.

### Added

- A missing sync manifest now aborts the update instead of silently skipping the
  rewrite. A skip would leave the freshly synced framework importing
  servicepack's own path, so nothing would build and the reason would be
  invisible.
- After rewriting, the update scans the files your repo actually owns (tracked
  plus untracked-and-not-ignored) and warns if any still reference the framework
  path — surfacing an incomplete manifest before you merge rather than at build
  time.

### Note for existing projects

Both scripts run from the freshly downloaded framework, so this fix applies on
the very next `make servicepack-update` — no intermediate release needed.

## v1.2.17 — 2026-08-06

The shipped `.servicepackupdateignore` now covers the framework's own repo
furniture. No code change.

### Fixed

- **A downstream update could add this repo's publication workflows to a
  project that must not have them.** The update is an rsync of the framework
  tree over yours, so a file servicepack ships that a downstream lacks is
  ADDED, not updated — and `mirror-and-archive.yml` force-pushes the repo to
  public GitLab and Codeberg and saves it to the Wayback Machine. servicepack
  is public; a downstream need not be, and on a private one that turns the next
  tag into a disclosure the archive does not forget. It is now ignored by
  default, along with the rest of the framework's own furniture:
  `issue-pull.yml` (relays issues from mirrors that do not exist),
  `pipeline.yml` (builds and releases THE FRAMEWORK, including publishing its
  agent skill to ClawHub), `.github/FUNDING.yml` (sponsors servicepack's
  author), `.agents` (the skill describing servicepack, which in a downstream
  tells an agent it is reading the framework), `.gitleaks.toml` (an allowlist
  names the untracked paths of the tree it belongs to) and `.dockerignore`
  (pairs with `Dockerfile`, already downstream-owned).

  The opt-out model was already right for a framework BASELINE a project
  diverges from — `.golangci.yml`, `Makefile`. It does not fit files that are
  wrong on arrival for every consumer, because then every project independently
  discovers the same mistake. Those now start ignored instead.

### Documentation

- **README's `.servicepackupdateignore` section said "Create a
  `.servicepackupdateignore` file".** The framework ships one, so that sent
  readers to create a file they already had. It now says the file is theirs
  from scaffold, tabulates every default entry with the reason, and states the
  consequence of the file being excluded from the sync: opt-outs are never
  overwritten, and new default entries never arrive automatically — an existing
  project copies them across by hand.
- The framework-vs-user file table listed `.github/` as wholly framework-owned,
  which is no longer true of the entries above.

## v1.2.16 — 2026-08-06

Two service-manager tests synchronized on `time.Sleep`. Both are fixed. Test
files only — no framework behaviour changed, but because these files ship with
the framework, every consumer inherits the flake until it updates.

### Fixed

- **`TestServiceManager_Stop` raced the manager.** It slept 5ms captioned "give
  services time to start", then called `Stop` and asserted every service had
  `Stop` called. `Stop` iterates `startGroups`, and a group lands there only
  after every service goroutine has launched and `waitGroupReady` has returned
  — so a `Stop` arriving before that append finds nothing to stop and every
  assertion fails. The sleep was the synchronization, not a courtesy pause:
  setting it to zero fails the test outright. It now waits for the manager to
  register the services, which is the precondition `Stop` actually has.
- **The same test used a 10ms context deadline as both a hang guard and a
  timer.** Under load the deadline could fire before `Stop` was ever called,
  ending the run for the wrong reason. `Stop` cancels the manager's context and
  closes each mock's channel, so the run ends on its own; the bound is now long
  enough to only ever mean "hung".
- **`TestServiceManager_Run` had the same sleep**, before asserting every
  service had `Run` called, and three of its rows spawned a goroutine that slept
  10ms and then cancelled the context — racing the manager to start the very
  services the row was about to assert on. Cancellation moved into the
  `stopMethod` switch, where it happens after the wait. The `contextSetup`
  field is gone: two rows returned a plain `WithCancel`, so the field's only
  real content was that race.

Under enough load to delay the manager past those windows, both tests failed for
reasons unrelated to the code under test — surfacing as an unexplained flake in
a consumer's CI, one package deep in an unrelated repo.

- **`TestIntegration_RetryWithDependencies` asserted an ordering the framework
  does not promise.** It required `db` to reach `Run` before `api`, and that was
  wrong roughly once in 600 runs. `ReadyNotifier`'s own contract says a service
  which does not implement it is "considered ready immediately after their
  goroutine is launched" — so the manager orders the LAUNCH of the groups, not
  the moment each service enters `Run`. `db`'s goroutine is started and has
  signalled before `api`'s exists, but it can be descheduled between that signal
  and its own `Run` body. Neither mock in that test implements `ReadyNotifier`,
  so the ordering was a coin flip weighted heavily enough to pass almost always.
  The assertion is gone; the guarantee is still covered by
  `TestServiceManager_ReadyNotifier`, which drives two `ReadyMockService`s and
  checks the exact sequence deterministically.

### Added

- **`.gitleaks.toml`** — this repo had no secret-scanning config at all. The
  allowlist covers only genuinely untracked paths (each one verified with
  `git ls-files`) plus `vendor/`, whose contents are third-party source
  reproduced verbatim. It allowlists by PATH only, never by a regex on the
  matched text, since the latter would silence a real credential in any file
  whose name merely looks like a fixture.

### Documentation

- **`.agents/skills/servicepack/SKILL.md` now says what `Dependent` alone
  actually guarantees.** It ordered the LAUNCH and the docs did not distinguish
  that from readiness, so "dependency-ordered startup" reads as a promise the
  framework only keeps when the dependency also implements `ReadyNotifier`.
  Spelled out, since that distinction is precisely what made the removed test
  assertion wrong.

## v1.2.15 — 2026-08-01

CI/infrastructure only. No code in this repo changed — the whole diff since v1.2.14 is under
`.github/workflows/`.

- **Split the pipeline** — building and publishing stay in `pipeline.yml`, and everything that
  leaves the host now lives in its own file beside it.
- **Mirrored to Codeberg as well as GitLab.**
- **Archived to the Wayback Machine, Software Heritage and archive.org.**
- **Issues opened on either mirror are copied back to GitHub** every six hours, and closed here
  when the original closes.
- **Pull requests are switched off on both mirrors** — they are force-pushed from GitHub, so
  anything merged there would be destroyed by the next sync. Issues and forking stay enabled.

## v1.2.14 — 2026-07-27

Codex README subsection was missing its install command.

- **Fixed the Codex subsection under "Agent integrations"** — it told readers to run
  `codex plugin marketplace add psyb0t/agents` and then stopped, never showing the actual
  install command. Added `codex plugin add servicepack@psyb0t` right after it.
- **Clarified the two invocation forms** — installed via the marketplace, the skill invokes
  as `$servicepack:servicepack`; picked up automatically from a repo's own
  `.agents/skills/` with no install, it invokes as plain `$servicepack`.

## v1.2.13 — 2026-07-27

Agent-client distribution manifests.

- **Added `.agents/.claude-plugin/plugin.json` and `.agents/.codex-plugin/plugin.json`** —
  metadata manifests that make the existing `.agents/skills/servicepack` skill installable
  as a plugin in Claude Code and Codex, rooted at `.agents/` so both clients discover the
  skill directory with no extra config.
- **Added an "Agent integrations" README section** with the copy-pasteable install commands
  for Claude Code (`claude plugin marketplace add psyb0t/agents` +
  `claude plugin install servicepack@psyb0t`), Codex
  (`codex plugin marketplace add psyb0t/agents`), and OpenClaw
  (`openclaw skills install @psyb0t/servicepack`).

## v1.2.12 — 2026-07-27

Self-hosted README badges.

- **Coverage / version / license badges are self-rendered SVGs** served from
  `raw.githubusercontent.com/psyb0t/servicepack/badges/*.svg` — no third-party
  render service. `make test-coverage` now writes the coverage percentage to
  `coverage-percent.txt`, the pipeline uploads it as an artifact, and a `badges`
  job bakes it into the SVG. The CI badge is switched to GitHub's native
  `badge.svg`.
- The `coverage-percent.txt` write lives in the framework-distributed
  `scripts/make/servicepack/test_coverage.sh`, so servicepack-based projects pick
  it up on `make servicepack-update`.

## v1.2.11 — 2026-07-27

Dependency bumps (own libraries).

- Bump `github.com/psyb0t/ctxerrors` 0.2.3 → 0.3.1,
  `github.com/psyb0t/goenv` 1.0.0 → 1.0.3, and
  `github.com/psyb0t/gonfiguration` 1.5.0 → 1.5.1. Vendored tree re-synced. No
  framework code changed.

## v1.2.10 — 2026-07-27

Ship a starter Dependabot config; keep it downstream-owned.

- **Added `.github/dependabot.yml`** — a starter age-gated Dependabot config.
  Weekly version-update PRs for `gomod` + `github-actions`, quarantined via
  `cooldown` (major 30 / minor 14 / patch 7 days) so a freshly-published release
  must sit public before it can be proposed. The starter excludes
  `github.com/psyb0t/*` from the cooldown (own packages update immediately) —
  change that prefix to your own module namespace.
- **`.servicepackupdateignore` now ignores `.github/dependabot.yml`** by
  default, so `make servicepack-update` won't overwrite your customized
  dependency policy. Delete that line if you'd rather the framework keep it in
  sync.

## v1.2.9 — 2026-07-27

Deflake the service-manager integration tests.

- **Fixed flaky integration tests.** Several `service-manager` integration tests
  cancelled the run after a fixed `time.Sleep` (100–200ms) and then asserted that
  the async startup sequence (retry, dependency-gated start, ordering) had
  finished within that window. On slow or loaded CI runners under `-race` the
  window was occasionally too short, so `TestIntegration_RetryWithDependencies`
  and its siblings flaked. They now wait for the expected end-state via a
  `waitThenCancel` helper that polls a condition (with a safety timeout) before
  cancelling — deterministic, no wall-clock assumption. Test-only; no framework
  code changed.

## v1.2.8 — 2026-07-26

README badges.

- Added pkg.go.dev reference + GitHub Actions CI status badges. No framework
  code changed.

## v1.2.7 — 2026-07-25

ClawHub agent skill + publish wiring.

- Added the ClawHub agent skill under `.agents/skills/` and a tag-gated
  `publish-to-clawhub` job in `pipeline.yml` that publishes it on release tags.
  No framework code changed.

## v1.2.6 — 2026-07-21

Make the self-updating updater future-proof: the newest update logic always
drives the update.

- `make servicepack-update` is a self-updating updater — the script that
  performs the update is itself one of the files being updated, so any policy
  baked into it (rsync excludes, dependency merge) could never protect the very
  update that installs the fix. `scripts/make/servicepack/servicepack_update.sh`
  is now a thin, stable bootstrap: it only verifies preconditions, fetches the
  latest framework, and hands off to `scripts/make/servicepack/do_update.sh`
  **from the freshly downloaded copy**.
- New `scripts/make/servicepack/do_update.sh` carries all the update policy
  (rsync exclude list, `go.mod` upgrade-only dependency merge, branch/commit
  flow) and always runs from the fresh download. Future policy changes now take
  effect on the first update that ships them — no manual bootstrap needed.

## v1.2.5 — 2026-07-21

Make `make servicepack-update` safe: never downgrade or drop the downstream
project's own dependencies.

- `scripts/make/servicepack/servicepack_update.sh` now excludes `go.mod`,
  `go.sum`, and `CHANGELOG.md` from the rsync sync. Previously rsync copied
  servicepack's own `go.mod` over the downstream project's, dropping every
  `require` line for the project's own deps; the subsequent `go mod tidy`
  then re-resolved those deps DOWN to the lowest version MVS allowed — a
  silent downgrade (e.g. a direct dep pinned at v3.44.0 fell to v3.8.1 via a
  leftover transitive floor). `CHANGELOG.md` documents the downstream
  project's releases, not servicepack's, so it is no longer overwritten.
- `scripts/make/servicepack/_post_update.sh` now merges the framework's
  direct `require` entries and `tool` directives into the downstream's
  existing `go.mod` UPGRADE-ONLY before running `make dep`: it adds what's
  missing, bumps what the framework raised, and never touches a dep the
  project already pins at an equal-or-higher version. Framework dependency
  bumps still land while the project's own deps stay intact.
- `.golangci.yml` is still framework-owned and overwritten on update; a
  project that has customized it opts out via its own
  `.servicepackupdateignore`, as before.

## v1.2.4 — 2026-07-21

Fix a flaky integration test in the service manager.

- `TestIntegration_FullStack` asserted that same-group services (`db`, `cache`)
  record their start before the next dependency group (`migrator`, `api`),
  but same-group services are launched as concurrent goroutines with no
  ordering guarantee between them. The test now waits for both group-0
  services to actually invoke their run callback (via `sync.WaitGroup`)
  before the next group's services record their start, removing the
  scheduler race. No production code changed.

## v1.2.3 — 2026-04-29

Fix a context leak on the app and service-manager run paths.

- `App.Run` and `ServiceManager.Run` now `defer cancel()` on the
  `context.WithCancel` they create, so the cancel func is always called
  even on early-return paths.
- Added a CI workflow restricting certain automation to repo collaborators.

## v1.2.2 — 2026-04-01

Lint fixes only, no functional changes. Retagged the same commit as v1.2.1.

## v1.2.1 — 2026-04-01

Fix `lll` tab-width and line-length lint issues across `app_test.go`,
`service_manager_test.go`, and the `hello-world` example service.

- Adjusted `.golangci.yml` line-length handling.

## v1.2.0 — 2026-03-31

Add lifecycle hooks to `App`.

- `App` gains `OnPreRun` and `OnPostStop` hooks, run immediately before
  `Run` starts and immediately after it returns, respectively.

## v1.1.3 — 2026-03-31

Same content as v1.2.0 (lifecycle hooks) plus build/CI housekeeping.

- Added `internal/app/app_test.go` coverage for the new hooks.
- Dockerfile and CI pipeline touch-ups; `go.mod` bump.
- README updates.

## v1.1.2 — 2026-03-22

Derive service import aliases from directory path instead of package name.

- Fixes an import alias collision when nested service directories share a
  package name.
- Added `example-nested/http` and `example-nested/grpc` — two example
  services with a shared package name (`server`) in different directories,
  demonstrating the fix.

## v1.1.1 — 2026-03-20

Read the module path from `go.mod` in service registration instead of
hardcoding `servicepack`.

- Fixes broken `service-manager` imports in projects generated via
  `make own` (renamed module path).

## v1.1.0 — 2026-03-20

Lazy service initialization via factory-based registration.

- `services.Init()` now registers factories instead of eagerly calling
  `New()` on every service.
- `./app run` instantiates all enabled services (filtered by
  `SERVICES_ENABLED`).
- `./app <service> <subcommand>` instantiates only that one service.
- Standalone CLI commands (`cmd/commands.go`) no longer touch any service.
- Docs updated to match.

## v1.0.7 — 2026-03-20

Remove app-level config, simplify runner config.

- Removed the now-dead `internal/app/config.go` and its `gonfiguration`
  usage.
- Renamed the `APPRUNNER_` env prefix to `RUNNER_`.
- Runner config now uses a struct `default` tag instead of a separate
  `SetDefaults` method / const block.

## v1.0.6 — 2026-03-19

Prevent `go tool modernize -fix` from mutating generated files.

- `lint_fix.sh` now runs `git checkout` on generated files after the
  `modernize -fix` pass, so codegen output isn't silently rewritten by the
  linter.

## v1.0.5 — 2026-03-19

Run Makefile scripts via `bash` explicitly.

- The `find_script` macro now prepends `bash`, so script file permissions
  no longer matter for `make` targets to work.

## v1.0.4 — 2026-03-19

Add `modernize` as a vendored Go tool; exclude generated files from its
output.

- Added `golang.org/x/tools/gopls/internal/analysis/modernize` as a
  `go tool` dependency (vendored) instead of `go run @latest`.
- Lint scripts invoke it via `go tool modernize`.
- `.gen.go` files are filtered out of modernize's suggestions.

## v1.0.3 — 2026-03-14

Add a `cmd/commands.go` extension point for custom CLI commands, plus
service-manager command wiring.

- `cmd/commands.go` is a user-owned file (never overwritten by framework
  updates) for defining project-specific `cobra.Command`s.
- `cmd/main.go` registers both the service manager's generated commands and
  the user's custom commands on the root CLI.
- `scripts/make/servicepack/own.sh` and `.servicepackupdateignore` updated
  to keep `commands.go` out of framework-update overwrites.

## v1.0.2 — 2026-03-14

Add `ReadyNotifier` — optional service readiness signalling.

- A service can implement `Ready() <-chan struct{}` to signal the service
  manager it has finished starting up before the manager launches the next
  dependency group.
- The service manager waits on `Ready()` (via `waitGroupReady`) before
  proceeding.

## v1.0.1 — 2026-03-14

Fix the release pipeline workflow.

## v1.0.0 — 2026-03-14

Initial public release of servicepack: a Go service-manager framework with
dependency-ordered startup, retries, allowed-failure services, and a
generated CLI.
