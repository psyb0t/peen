# Changelog

All notable Peen changes per release. Versions follow [semver](https://semver.org). Peen release history starts at v0.1.0. Entries below that document the Servicepack baseline Peen was created from.

## v0.27.1 (2026-10-21)

Builds Peen with Go 1.26.10 to fix a reachable standard library vulnerability, and moves the README's detailed sections into `docs/`. The v0.27.0 tag never produced a published image or release, so this is the first release that ships the v0.27.0 changes. No configuration or API migration is needed.

- Builds with Go 1.26.10, which makes `crypto/x509` distrust compromised intermediate certificates (GO-2026-6809). `govulncheck` reports it as reachable from Peen through the Docker Engine API client. `go.mod` now requires Go 1.26.10.
- Changes the README to a short overview with install, first run, a security summary, and links. The command line guide moves to `docs/command-line.md`, the browser control surface to `docs/control-surface.md`, and the full security section to `docs/security.md`. The README also fits Docker Hub's description limit again, which the v0.27.0 README exceeded.

## v0.27.0 (2026-10-20)

Run a turn from the command line in a workspace's durable session, with streamed output and the same configuration as the control surface. No configuration or API migration is needed.

- Adds `peen --prompt "prompt"` through the installed command and `peen exec [flags] (--prompt <prompt> | <prompt>)` through both the binary and the installed command. They reuse a running controller or start one inside the command with an in-memory API and no TCP or metrics listener, then stop it after the turn. A message sent during a running turn is queued into it, and the command follows that turn to its end.
- Adds JSONL output preserving every server frame, `--output text`, stdin prompts with `-`, per-turn model and system prompt flags, workspace and agent space selection, and interrupt cancellation. The installed command also accepts `--control-space host|docker` and exported configuration overrides for one run.
- Prevents two controllers from opening the same state directory at once. The controller holds `<PEEN_STATE_DIR>/controller.lock` until shutdown, and a second controller fails while the lock is held. A crash releases the lock automatically.
- Fixes `peen start` blaming the model provider for an unrelated start failure when the controller log held `401` or `403` inside a longer number, such as a path or a byte count. The hint now matches those codes only as whole numbers.
- Changes the contributor rules in `.agents/rules/` to cover only Peen's own conventions, with the Go lint, error, and logging rules merged into `go.md`. `.claude` now links to `.agents`, so Claude Code reads the same rules.
- Changes the schedule of the workflow that relays issues opened on the GitLab and Codeberg mirrors, spreading its runs across the day.

## v0.26.4 (2026-10-19)

v0.26.3 failed CI and published no image, release, or skill. v0.26.4 ships the v0.26.3 changes below with this fix. No configuration or API migration is needed.

- Fixes the idle wake re-check that v0.26.2 added starting background work after every completed turn, even with no notice pending. It now reads the session's pending notices before the turn returns and starts background work only when one is pending.

## v0.26.3 (2026-10-19)

Long turns no longer die partway through. The session database schema changes, but existing databases keep working: on startup Peen adds the new `content_blobs` table, and records written before the upgrade read as before.

- Fixes turns that ran more than about 20 tool rounds ending `interrupted`, with half-finished edits left in the workspace. Every saved step of a turn stored the whole turn context again, so a turn's workflow history grew with the square of its rounds until reading it exceeded the 32 MiB worker protocol frame, and the session's worker was stopped. Peen now stores each long JSON string and each long run of array elements once, in the new `content_blobs` table of `peen.db`, and saves a reference in its place, in the workflow history and in the stored execution steps. Runs are cut by content, so a message history that grew by a few messages shares every earlier run with the previous step. A 32-round turn that averaged about 100 KB of workflow history per step before now averages about 14 KB.
- Fixes a session that already holds such a history failing every later turn. A worker now reads workflow history in pages of at most 8 MiB, and a reply too large for one frame fails only that call instead of closing the worker's connection.
- Fixes an event handler's wake logging "event wake turn failed: session already has an active turn" when its event arrived just as a turn ended. The event and the re-check that v0.26.2 added after each turn both started a wake turn, and the second one failed. A session now runs one wake decision at a time, and a re-check that arrives during a wake runs when that wake's turn returns.
- Fixes `peen doctor` passing a state directory whose worker socket paths are longer than the 107 byte Unix socket limit. It now reports the length and says to set `PEEN_WORKER_SOCKET_DIR`, and `peen start` no longer blames the model provider for that failure or any other failure whose log does not name the provider.
- Fixes `PEEN_WORKER_SOCKET_DIR` and `PEEN_LOG_DIRECTORY` outside the state directory in the Docker control space. The `peen` command did not mount them into the controller container. It now mounts them and creates them with mode `0700` when they are missing.
- Fixes setup leaving the setgid bit on Peen's configuration and state directories when the home directory has it, which made `peen doctor` fail a fresh install. The mode fix `peen doctor` prints now clears that bit too.

## v0.26.2 (2026-10-18)

A release now ships the exact image that passed CI on `main`. The tag's run waits for the `main` run of the same commit, re-tags that run's image instead of building it again, and publishes the GitHub release with the tag's notes. No configuration or API migration is needed.

- Changes the published images to report `sha-<commit>` as their version instead of the release tag or `latest`. The release image is the `main` image, re-tagged, so it names `psyb0t/peen:sha-<commit>` as its default worker image, which holds that same image. Installs made with `install.sh` or `peen upgrade` pin `PEEN_WORKER_IMAGE` and are unaffected.
- Changes the CI pipeline so a release tag is not rebuilt or retested. A tag whose commit failed CI on `main`, or never ran there, publishes nothing.
- Fixes GitHub releases carrying only a "Full Changelog" link. The release body is now the annotated tag's notes.
- Fixes the browser control surface showing the stray text `height: 100dvh; padding: 1rem;` below the page.
- Fixes tests that timed out on 4-CPU CI runners under the race detector. Positive waits in the unit tests use generous shared timeouts instead of short fixed windows.
- Fixes an event handler's wake being lost when its event arrived as a turn was finishing: the turn had no tool boundary left to deliver it at, so it stayed queued until someone sent the session a message. The session now re-checks its queued notices when that turn ends completed or failed on its own and wakes if one still qualifies; a turn ended by Stop is not followed by that re-check.

## v0.26.1 (2026-10-17)

`make` targets run on machines with fewer than 6 CPUs again, including CI runners. From v0.20.0 on, every CI run failed at its first dev container, so v0.20.0 through v0.26.0 published no Docker image, GitHub release, or `peen` skill. No configuration or API migration is needed.

- Fixes `make` targets failing on machines with fewer than 6 CPUs, including GitHub's runners. The `Makefile` passed a fixed `--cpus=6` to every dev container, and Docker refuses a CPU limit above the host's CPU count.
- Changes the dev container CPU cap to come from an optional, gitignored `.env.dev`. Copy `.env.dev.example` to `.env.dev` and set `DEV_CPUS` to cap dev containers; without the file they run uncapped. `DEV_CPUS` on the command line still overrides it.

## v0.26.0 (2026-10-16)

Session work keeps running after every client disconnects, and a client that reconnects replays what it missed from a new per-session feed. REST moves to `/api/v1`, the WebSocket moves to `/ws/v1`, and `api/asyncapi.yaml` now defines the WebSocket contract. Before upgrading, back up `PEEN_STATE_DIR`, update every client's URLs, and give the generated Go client the new server URL. The session database schema changes again, so the old databases have to be removed; the breaking changes below list the exact files.

- Breaking: moves REST from `/v1/...` to `/api/v1/...`. An old path now answers a JSON `404 NOT_FOUND` instead of the API or the browser app. Pass `.../api/v1` as the server URL to the generated Go client in `pkg/http/api/client`. Metric route labels change to match.
- Breaking: moves the WebSocket from `/v1/ws` to `/ws/v1`.
- Breaking: changes two `agent.run.*` event payloads to Peen-owned camelCase. `agent.run.assistant.message`'s `event` is now `{role, content, toolCalls, reasoning?, providerReasoning?, origin?}`. `agent.run.message.injected`'s `event` is now `{type, content, phase, tool, callId, round}`.
- Breaking: a feed frame's `timestamp` is now the stored row's creation time rather than the time it was rendered, the same live and on replay.
- Breaking: changes the session database schema again, adding feed sequence columns. Back up existing state before upgrading. If startup reports a schema mismatch, stop Peen and remove `peen.db`, `peen.db-wal`, `peen.db-shm`, `workflows.db`, `workflows.db-wal` and `workflows.db-shm` from `PEEN_STATE_DIR`. Keeping `workflows.db` still starts, but leaves the removed sessions' workflow instances orphaned in it. The startup error now names all six files. Earlier development schemas are not migrated.
- Breaking: a session's work no longer stops when every client disconnects, including the last session-filtered WebSocket. Only `POST /api/v1/session/cancel`, the browser Stop control, `peen session stop`, cancelling a child, `peen stop`, an embedded `Close`, a failed turn, or a crash stop it now. An unattended session keeps calling the model and spending provider budget; see [Security](docs/security.md#unattended-sessions).
- Breaking: a live WebSocket frame's `id` is now the stored record's id instead of a random value, and every sequenced frame carries a new `metadata.sequence`, its position in the session's feed. Sequenced frames carry `triggeredBy: null` instead of the all-zero UUID. `message.completed` and `message.failed` stay unsequenced, keep a random `id`, and still name the client frame in `triggeredBy`.
- Breaking: `peen session attach` now prints `sequence<TAB>type<TAB>data` per line instead of its previous format, and reconnects automatically after an unexpected drop, catching up from the session feed. Its new `--cursor <n>` flag replays from sequence `n` first.
- Adds `GET /api/v1/session/feed?cursor=&limit=`, which replays a session's sequenced live frames after `cursor`, oldest first: `{items, limit, nextCursor, hasMore, head}`. A client that reconnects tracks the last sequence it applied, drops anything at or before it, and pages the feed from there until `hasMore` is false.
- Adds `feedSequence` and `activeTurnFeedSequence` to `GET /api/v1/session`. `feedSequence` is the session's newest feed sequence, and `activeTurnFeedSequence`, set only while a turn runs, is that turn's first feed sequence, so a client can replay from the sequence before it to rebuild the running turn's live view.
- Adds a `notice.created` live frame for every session notice Peen records, including notices that never reach the model. Its data is the stored notice.
- Adds `api/asyncapi.yaml`, the AsyncAPI 3.0.0 contract for the WebSocket, with generated Go types in `internal/pkg/http/wsapi` and TypeScript types in `web/src/lib/ws/generated.ts`. Peen validates every outgoing frame against it and logs a mismatch instead of dropping the frame; the web client skips a frame that fails validation and still advances its cursor past it.
- Fixes `agent.run.*` steps, documented as live WebSocket frames but never sent in the worker architecture. Every connected client now receives them live, each with a sequence.
- Changes the browser control surface to drop its session-filtered WebSocket. It reconnects to the global socket automatically with backoff and catches up from the session feed; `?sessionId=` on `GET /ws/v1` remains a pure filter that does not affect whether work continues.
- Updates the build tooling to add `github.com/psyb0t/gen-asyncapi` v0.2.0, which generates the WebSocket's Go and TypeScript contract types from `api/asyncapi.yaml`.

## v0.25.0 (2026-10-15)

Peen runs root, child and grandchild conversations through SQLite-backed go-workflows, retaining context and completed activity results across crashes. Stop pauses unfinished work until explicit continuation. No configuration or API migration is needed.

- Breaking: changes the session database schema for durable execution, inboxes, turn ownership and job ownership. Back up existing state before upgrading. If startup reports a schema mismatch, stop Peen and recreate `peen.db`, `peen.db-wal` and `peen.db-shm` from `PEEN_STATE_DIR`. Earlier development schemas are not migrated.
- Adds separate `workflows.db` storage owned by the controller. Session workers use authenticated, generation-bound RPC; replaced workers cannot overwrite recovered work.
- Preserves root and nested agent identities, ancestry, instructions, model settings, remaining round budgets, directories, skills, inboxes, summaries and transcripts after controller or worker loss. Completed model, tool and hook results are reused. Interrupted effects retain an uncertain outcome for inspection before retry.
- Adds `searchExecutions` and `readExecution` for bounded, session-scoped history and context lookup across retained execution states. Reading history leaves stopped work stopped. Oversized reads return a tool error so the agent can request a smaller page or context chunk.
- Adds `resumeExecution` and `resolveExecutionActivity` for explicit continuation and inspected resolution of uncertain effects. Resume preserves saved identity and round limits, and refuses stale activity attempts or control revisions.
- Changes Stop to persist a pause before cancelling current activities. Paused trees remain inactive across restart, reconnect, child reports and job completion; unrelated user work can start another turn. Foreground `runCommand` cancellation kills its process group, while commands whose running job handles already returned remain detached.
- Changes client disconnect behavior: losing the last session-filtered WebSocket pauses its work, while another connected client preserves continuity. The browser keeps its selected session connected separately from the global event feed. Reconnecting leaves paused work stopped.
- Preserves accepted root and child input, questions, wait deadlines, completion reports and deferred messages across restart. Repeated requests reuse their saved identities, and messages arriving during finalization receive another model round within the existing budget.
- Commits activity results with their transcript, stream and completion records. Recovery retains stream indexes and final event identities without repeating completed tool effects or terminal notifications.
- Retains separate opening, tool, compaction and terminal hook checkpoints. Completed hooks and prompt injections survive retries; interrupted effects require inspection. Stop runs no success, failure or terminal hooks for the paused task.
- Runs embedded sessions through the same durable workflow schedule. Restored roots respect shared turn capacity. Close pauses unfinished work and drains workers before closing storage; failed startup releases owned resources. Workflow logs retain the caller's logger and audit sink.
- Preserves directory-hop harness state, effective prompts, file-read observations and virtual filesystem paths during restoration. Compaction retains completed history links and original summarizer settings while leaving interrupted messages intact.
- Fixes child cancellation and exhausted round budgets leaving runs active. Cancellation stops the unfinished subtree, retains its history and reports its terminal outcome without restarting it.
- Updates common-go to v0.8.0 and uses its shared modernc SQLite adapter, preserving pure-Go builds and constraint-error mapping alongside workflow storage.
- Restores Go race detection in every Go test target, including the CI coverage run.
- Keeps Docker build output in integration-test logs and shows setup progress during `make test-api`.
- Updates OpenTelemetry modules to v1.45.0 for GO-2026-6505 (CVE-2026-81870), which addresses exporter endpoint URLs appearing in diagnostic logs.

## v0.24.0 (2026-10-12)

Interrupted child agents resume when their session gets a replacement worker, keeping their run IDs and recorded progress. No configuration, API or database migration is needed.

- Adds child recovery with saved instructions, model, tool allowlist, current directory, summaries, transcript and remaining round budget. Accepted inbox messages and pending passive waits survive a crash; uncertain unfinished tools return an error instead of repeating their effects automatically.
- Fixes long-session directory recovery transferring every transcript page before a turn. Recovery reads only the latest successful paired directory result.
- Fixes child directory changes losing their canonical path when tool output is truncated. The directory and its tool result now commit together.
- Fixes a replaced worker being able to overwrite a recovered child's checkpoints, and allows cancellation of an interrupted child before it resumes.
- Fixes a worker crash leaving its session busy, including a crash before the worker acknowledges turn acquisition. Peen records the worker owner when it creates the turn and releases that turn when the connection ends.

## v0.23.0 (2026-10-12)

Agents now load their harness from their current directory and its ancestors. Use `changeDirectory` before working in a subdirectory or another project. Move any instructions that must always apply into an ancestor or the configuration layer. No HTTP API or configuration migration is needed.

- Breaking: stops loading descendant `AGENTS.md` files before an agent enters their directory. Directory hops reload rules, skills, named agents, event handlers and hooks together, and leaving a directory removes its local definitions from the active harness.
- Adds `changeDirectory` for persistent agent directory changes, including `{"directory":"."}` to refresh edited harness files. Root hops survive later turns, compaction and restarts; child agents inherit the current directory and then change independently.
- Fixes relative file paths in a nested virtual directory resolving from the virtual root instead of the agent's current directory.

## v0.22.0 (2026-10-12)

Go embedders can own and close the engine, address multiple sessions, replay stored work, subscribe across turns, and supply tools, hooks, model resolution and workspace filesystems. Before upgrading an existing embedded runtime, set StateDirectory to its old database directory and move trusted harness configuration into a separate ConfigDirectory. HTTP, WebSocket and deployment configuration keep their existing contracts.

- Breaking: separates embedded SQLite and hook state from ConfigDirectory. The default StateDirectory is ConfigDirectory with a `-state` suffix. Equal or nested directories are refused; explicitly migrate the old combined directory before upgrading.
- Breaking: changes the embedded MaxEventWakesPerHour zero value to the deployment limit of 60. Set a negative value to retain unlimited wake turns.
- Fixes embedded runtimes retaining SQLite, child agents and command jobs after their caller finishes. Runtime.Close refuses new work, drains calls, stops children and jobs, and releases owned resources; deadline cancellation can be followed by another Close to finish cleanup.
- Adds explicit Workspace, in-memory SQLite with temporary hook state, tuning groups, prompt values, disabled core definitions, runtime-local logging, an audit sink and caller-owned Prometheus registration to embedding options.
- Adds Session handles with open, resume, list, send, cancellation, transcript and provider replay, snapshots, notices, job controls and child-agent controls. Existing Runtime message methods continue to use the initial session.
- Adds bounded live subscriptions carrying session and request IDs across root, wake and child turns. Slow consumers can detect dropped frames and replay durable records.
- Adds schema-validated custom Go tools, disabled workspace tools, Go hook callbacks, dynamic model lookup, immutable harness layers and a command runner shared by jobs and executable hooks.
- Adds OSFS, RootFS and read-only io/fs adapters for embedded workspace tools and harness discovery. Virtual workspaces use a caller-supplied identity and omit process tools and executable hooks unless a command runner is supplied.
- Updates the embedding guide with lifecycle, session, extension and filesystem examples.

## v0.21.0 (2026-10-11)

Child agents now run beside the agent that launched them instead of blocking it. `launchAgent` returns at once, a child keeps running after its turn ends, and agents talk to each other and to you with `sendMessage`, `waitMessages`, `stopAgent`, and `ask`. A child of the root reports its final answer through `agent.finished` or `agent.failed`, which now carry the answer and start a turn when the root is idle. Before you upgrade, change any prompt, skill, or hook that reads `response` or `cancelled` from a `launchAgent` tool result, and add an event handler with `delivery: queue` for `agent.finished` and `agent.failed` if you do not want a finished child to start a turn. No configuration or API migration is needed otherwise.

- Breaking: `launchAgent` returns as soon as the child runs, with `{"agentRunId", "name", "definition", "parentAgentRunId"}`. The `response` and `cancelled` fields are gone. The child's final answer reaches the root as an `agent.finished` or `agent.failed` session event, and reaches a parent child as a `<message kind="result">` block, or `kind="failed"` or `kind="cancelled"`. Call `waitMessages` to wait for it.
- Breaking: a child keeps running after the turn that launched it ends. Cancelling a turn stops the children it started, `POST /v1/session/cancel` also stops children of turns that already ended, and a child that ends stops the children it launched.
- Breaking: Peen publishes `agent.finished` and `agent.failed` only for children of the root conversation. Their data adds `state`, the final answer in `response` cut to `PEEN_MAX_EVENT_DATA_BYTES` with `responseTruncated`, and `failureDetail`. They ask to wake the root, and an idle root wakes for them without an event handler file. `agent.failed` for a cancelled child stays queued. A handler for the type with `delivery: queue` stops the wake.
- Breaking: a child's task, and every message one agent sends another, arrives as a `<message from="agent" name="..." run="...">` block naming the sender. The first message of a child's stored transcript is that block. The run record's `task` stays the plain task. The default system prompt names the block.
- Fixes `POST /v1/session/agents/{agentRunId}/cancel` only recording the request for a child that ran in a session worker, so the child ran on until it finished. The request now reaches the worker and stops the child and its descendants.
- Adds the tools `sendMessage`, `waitMessages`, `stopAgent`, and `ask`. Every agent has them, whatever its `allowed-tools` lists. `sendMessage` writes to any running agent in the session by its run ID, such as a child or the parent, or to `root`, which the root reads as an `agent.message` session event. `ask` waits for an answer. A child of the root asks through an `agent.question` session event, a deeper child asks its parent, and the root asks you, with a `question.asked` frame and your next message as the answer.
- Adds `PEEN_ASK_TIMEOUT`, the longest time `ask` waits for an answer. The default `0` sets no limit of its own, and `ask` and `waitMessages` never wait longer than 30 days.
- Adds `to` to the WebSocket `message.send`. It sends the message to a running child agent, which reads it after its current tool call as a `<message from="user">` block. It starts no turn, completes as queued, and fails with `AGENT_FINISHED` for a child that ended, `AGENT_NOT_FOUND` for an unknown run, or `USER_MESSAGE_QUEUE_FULL` for a child whose inbox is full.
- Adds the `waiting` and `interrupted` state filters and the `parentAgentRunId` filter, an agent run ID or `root`, to `GET /v1/session/agents`, and the `waiting` and `question` fields to every agent run record. A run waits while it has asked a question that nobody answered.
- Adds the protocol events `question.asked`, `question.answered`, `agent.run.message.queued`, `agent.run.message.delivered`, `agent.run.message.undelivered`, `agent.run.question`, and `agent.run.answer`.
- Adds a running-agents panel to the control surface, above the composer. It lists each running child with the question it waits on and a Stop button. A Send to picker writes to a running child, and the root's open question shows above the composer.
- Adds the core skills `delegation` and `plan-and-delegate`, which describe handing work to child agents, talking to them, and checking their results. `PEEN_DISABLED_CORE_SKILLS` accepts both names, and `all` removes them too.
- Changes a stopping worker to cancel its running children and record them as cancelled before it exits, for up to 10 seconds.

## v0.20.0 (2026-10-11)

`install.sh` now installs a `peen` command that runs Peen for you, in a container or as a host binary, and gives each workspace's agent its own container or runs it next to Peen, as `docs/where-it-runs.md` explains. After installing, `peen config edit` adds a model key and `cd your/project && peen` opens it. Docker profiles can now set `docker: readonly`. Peen also refuses a workspace that would expose its own state or configuration directory. If you used `install.sh` to build a host binary, use `make install` from a clone instead. If you open a workspace that contains `PEEN_STATE_DIR` or `PEEN_CONFIG_DIR`, open the project directories inside it instead. No API migration is needed.

- Breaking: `install.sh` no longer clones the repository and builds a binary into `~/bin`, and it ignores `PREFIX` and `REF`. It installs the `peen` command in `~/.local/bin`, or `/usr/local/bin` with `--system`, pins the newest release image, and writes `~/.config/peen/.env`. It refuses to run as root without `--system`. Use `--version vX.Y.Z` to pick a release and `--rolling` to follow `latest`. To build from source, clone the repository and run `make install`.
- Breaking: Peen refuses to open a workspace that is, contains, or sits inside `PEEN_STATE_DIR`, `PEEN_WORKER_SOCKET_DIR`, or `PEEN_LOG_DIRECTORY`, or is or contains `PEEN_CONFIG_DIR`, with `403 WORKSPACE_NOT_ALLOWED`. Before this, opening such a directory in a Docker agent space mounted the database with every session's transcript and every other session's worker socket into that worker. Open the project directories inside it instead.
- Fixes published images reporting the version `dev`, which made a controller without `PEEN_WORKER_IMAGE` start Docker workers from `psyb0t/peen:dev`, an image that does not exist. Images now carry the tag they are published under, or `latest` for builds from `main`, so a release starts workers from its own image.
- Adds the `peen` command. `peen`, `peen .`, or `peen <dir>` starts Peen when needed, opens the session for the directory, and prints a link to it. `peen setup`, `start`, `stop`, `restart`, `status`, `logs`, `run`, `config`, `doctor`, `upgrade`, and `uninstall` manage the installation, and `peen session` and `peen control` run the server's own client commands with the installation's configuration. `docs/deployment.md` lists every flag and command.
- Adds `PEEN_WORKER_IMAGE` pinning to the installer. It sets the variable to the installed release, and `peen setup` and `peen upgrade` move it to the new release only while it still names the old one. Clear it with `peen config set PEEN_WORKER_IMAGE ''` to let Docker profile images apply.
- Adds `--control-space host|docker` and `--agent-space control|docker` to the installer and to `peen setup`. The default is `docker` for both. The installer refuses `host` with `control` when your user can reach a Docker daemon, and requires `--workspace-root` for `docker` with `control`. The host control space takes the binary out of the published image over the registry's HTTP API, checking each layer's sha256, so it needs no Docker daemon.
- Adds `docker: readonly` for Docker profiles. The worker gets a Docker API at a socket in its own session directory, with `DOCKER_HOST` pointing at it, that answers only read requests such as listing and inspecting containers, images, networks, and volumes, stats, and events. It refuses creating, starting, exec, attach, logs, file copies, and image pulls and builds, and replaces environment values in inspect output with `<redacted>`. `GET /v1/agent-spaces` reports it with `hostRootEquivalent` false and a capability warning.
- Adds `peen config init --env-file PATH`, which writes the commented `.env` template with the given directories, workspace root, default agent space, images, and host identity. It never overwrites an existing file. `.env.example` is now generated from the same template.
- Adds a session link to the control surface. `/?session=<id>` shows that session once you connect. The link never carries the API token.
- Changes the dev container CPU cap for Peen's `make` targets to 6 CPUs by default. `DEV_CPUS` still overrides it.

## v0.19.1 (2026-10-11)

A `control` agent can no longer read `PEEN_API_TOKEN` or a provider key from the `/proc` files of the controller or its own worker. A `control` agent still runs as the controller's user, so it can still read and change `PEEN_STATE_DIR`, which holds every session's transcript, and `docs/where-it-runs.md` lists what else it can reach. A `PEEN_` variable that is not a worker setting and is not named by an upstream's `apiKeyEnv` no longer reaches a `control` worker or the commands its agent runs, so move anything your agent's commands read from such a variable to a name without the `PEEN_` prefix. No API migration is needed.

- Fixes a `control` agent reading `PEEN_API_TOKEN` and every provider `apiKeyEnv` variable through `/proc/<pid>/environ` of the controller or of its session worker. On Linux the controller and `peen worker` now mark themselves non-dumpable before anything else, so the kernel refuses that read for any process of the same user. Peen refuses to start when it cannot set this. Root and processes with `CAP_SYS_PTRACE` can still read a non-dumpable process.
- Fixes a `control` worker inheriting the controller's whole environment. It no longer receives `PEEN_API_TOKEN`, `PEEN_STATE_DIR`, `PEEN_DOCKER_SOCKET`, `DOCKER_HOST`, the listen addresses, the host identity and log settings, or any other `PEEN_` variable a worker does not read. It keeps the provider credentials named by `apiKeyEnv` and every variable without the `PEEN_` prefix.
- Updates `golang.org/x/crypto` to v0.57.0, with `golang.org/x/sys` v0.48.0, `golang.org/x/sync` v0.23.0, and `golang.org/x/text` v0.42.0. GO-2026-5932 has no fixed version and covers only the `x/crypto/openpgp` package, which Peen does not import.

## v0.19.0 (2026-10-11)

Every name Peen owns is now camelCase, execution profiles become agent spaces with explicit Docker grants, and Peen refuses the `control` agent space on a controller that can reach a Docker daemon. This is a clean break with no compatibility layer, and some old settings fail open instead of failing startup. Old environment variables are ignored without a warning, and a `hooks.yaml` that still uses an old key is skipped, so its deny rules stop applying. Before you upgrade, stop Peen and delete `peen.db`, `peen.db-wal`, and `peen.db-shm` from `PEEN_STATE_DIR`, which discards every stored session, so copy the three files first if you need the history. Then translate `PEEN_EXECUTION_PROFILES` and `PEEN_DEFAULT_EXECUTION_PROFILE` as the bullets below describe, rename every `hooks.yaml` key and `match.tool` value, point a pinned `PEEN_WORKER_IMAGE` at a v0.19.0 image, and update any client that reads tool names, event types, or the agent-space API.

- Breaking: tool names are camelCase: `listFiles`, `searchText`, `readFile`, `writeFile`, `editFile`, `applyPatch`, `movePath`, `makeDirectory`, `removePath`, `runCommand`, `listJobs`, `readJobOutput`, `waitJob`, `signalJob`, `useSkill`, `launchAgent`. Update `allowed-tools` in agent files and `match.tool` in hooks. A `match.tool` that still names an old snake_case tool, such as `run_command`, is not an error and never matches, so the guard stops working without a warning.
- Breaking: hook event names are camelCase: `preToolUse`, `postToolUse`, `toolUseFailure`, `pre<Tool>`, `post<Tool>`, and `<tool>Failure` for each file tool (for example `preWriteFile`, `postWriteFile`, `writeFileFailure`), `preUserMessage`, `postUserMessage`, `sessionStart`, `turnStart`, `turnStop`, `turnCancelled`, `preCompact`, `postCompact`. The `event` field of hook command input uses the same names.
- Breaking: `hooks.yaml` keys and action types are camelCase: `workingDir`, `eventType`, `onFailure`, `emitEvent`. Peen skips a file that still uses an old event name or key as a whole and reports it only as a `harness.warning`, while turns keep running, so its deny rules stop applying until you rename them. A file with the removed `timeout_seconds` key is skipped the same way, with a `harness.warning` naming the unknown key. The `hook.action.failed` event data uses `hookName` and `actionType`.
- Breaking: Peen's own event types are camelCase inside each dotted part: `userMessage.created`, `userMessage.queued`, `userMessage.delivered`, and the child agent event source `agent.launchAgent`. The Anthropic streaming frames carried over the WebSocket keep their own names.
- Breaking: execution profiles are now agent spaces. An agent runs in `control`, where Peen itself runs, or in a named Docker profile with its own container. `PEEN_EXECUTION_PROFILES` is replaced by `PEEN_DOCKER_PROFILES`, a JSON array of Docker profiles, and `PEEN_DEFAULT_EXECUTION_PROFILE` by `PEEN_DEFAULT_AGENT_SPACE` (`control` or `docker:<profile>`). A built-in `default` Docker profile exists unless you define one with that name. Peen ignores the old variables without a warning, so a controller that still sets only them runs with the built-in profiles and none of your mounts or network limits. See `docs/where-it-runs.md`.
- Breaking: translate each old profile by hand. Drop `kind`. A `native` profile is now the built-in `control` agent space and needs no entry. A `docker` profile named `sandbox` becomes the Docker profile `sandbox`, which clients name as `docker:sandbox`. `allowDockerSocket: true` becomes `"docker": "full"`, `allowPrivilegeEscalation: true` becomes `"sudo": true`, and `allowNetwork: true` becomes `"network": "bridge"`. `PEEN_DEFAULT_EXECUTION_PROFILE=native` becomes `PEEN_DEFAULT_AGENT_SPACE=control` or nothing, and `PEEN_DEFAULT_EXECUTION_PROFILE=sandbox` becomes `PEEN_DEFAULT_AGENT_SPACE=docker:sandbox`.
- Breaking: the network default flips. An old profile without `allowNetwork: true` had no network. The new `network` grant defaults to `bridge`, so write `"network": "none"` to keep a profile offline.
- Breaking: a Docker profile's grants are `docker` (`none` or `full`), `network` (`none`, `bridge`, or `host`), and `sudo` (`true` or `false`), replacing `allowDockerSocket`, `allowNetwork`, and `allowPrivilegeEscalation`. Unknown keys fail startup. `docker: readonly` is refused for now. Each risky grant is listed in the profile's capability warnings, and the control surface shows the grants next to each agent space.
- Breaking: when the controller can reach a Docker daemon, the `control` agent space is not offered and `PEEN_DEFAULT_AGENT_SPACE=control` fails startup, because an agent running beside the daemon can take over the host. Peen checks `PEEN_DOCKER_SOCKET`, a `unix://` `DOCKER_HOST`, and `/var/run/docker.sock`, and counts any socket where the Docker API answers `GET /_ping`. A `tcp://` `DOCKER_HOST` that accepts a connection counts too. With a daemon and no default set, new workspaces use `docker:default`. Without one the default stays `control`. A session whose agent space is no longer offered fails each turn with `AGENT_SPACE_NOT_ALLOWED` until you move it with `POST /v1/session/reconfigure`.
- Breaking: Peen refuses to start when it can reach a Docker daemon but cannot build the Docker worker launcher, because no agent space could run. The error names `PEEN_HOST_USERNAME` and `PEEN_HOST_HOME`. A daemon reachable only through a `tcp://` `DOCKER_HOST` also stops startup, because Peen launches workers only through a local socket.
- Breaking: the API uses agent-space names. `GET /v1/agent-spaces` replaces `GET /v1/execution-profiles` and omits `control` when the controller can reach a Docker daemon. `GET /v1/session/agent-space-decisions` replaces `GET /v1/session/profile-decisions`. The `profile` request field of `POST /v1/sessions/open` and `POST /v1/session/reconfigure` is `agentSpace`. Renamed response fields: the session's `executionProfile` is `agentSpace`, a worker generation's `profile` and `profileRevision` are `agentSpace` and `agentSpaceRevision`, a decision's `fromProfile` and `toProfile` are `fromAgentSpace` and `toAgentSpace`, and an agent space's `capabilityWarning` string is `capabilityWarnings`, an array. The booleans `allowDockerSocket`, `allowNetwork`, and `allowPrivilegeEscalation` are replaced by `docker`, `network`, and `sudo`, and a Docker space adds `profile`. `kind` is `control` or `docker`, where it was `native` or `docker`. `EXECUTION_PROFILE_NOT_ALLOWED` is now `AGENT_SPACE_NOT_ALLOWED`.
- Breaking: the controller-to-worker protocol uses camelCase frame kinds, commands, and fields. A controller and a worker image from different releases cannot talk, so point a pinned `PEEN_WORKER_IMAGE` or a profile `image` at a v0.19.0 image.
- Breaking: the SQLite schema is rebuilt from one migration per table. On open, Peen compares the database's schema with the one its migrations build and refuses a database that differs, with an error naming `peen.db`, `peen.db-wal`, and `peen.db-shm`. Deleting them discards every stored session, transcript, job record, and model call. Copy the files first if you need the history.
- Fixes a Docker profile's `image` never taking effect. Peen now uses `PEEN_WORKER_IMAGE`, then the profile's `image`, then the image published alongside the controller's build.
- Fixes `runCommand` and background jobs inheriting `PEEN_API_TOKEN` and every provider `apiKeyEnv` variable, which a command such as `env` printed into the transcript and the live feed. Commands now start with those variables removed. A `control` agent runs as the controller's user and can still read them through `/proc`.
- Fixes `docker: full` always mounting the host's `/var/run/docker.sock`. Peen now mounts the socket the controller itself uses at `/var/run/docker.sock` in the worker, and refuses to start, instead of failing at the first launch, when a `docker: full` profile meets a socket owned by group 0.
- Fixes the control surface showing an empty agent space in the session header for a session that has none recorded. It now shows "Controller default".
- Removes the startup checks that named settings removed in earlier releases (`PEEN_LOG_RETENTION_DAYS`, `PEEN_WORKSPACE_ROOTS`, `PEEN_ENABLE_WORKSPACE_HOOKS`), the hooks `timeout_seconds` warning, and the special message for `PEEN_AGENT=default`. Those environment variables are now ignored, and `PEEN_AGENT=default` resolves like any other plain name at each turn.
- Adds a contributor naming rule (`.agents/rules/naming.md`) with tests that fail on any non-camelCase tool name, hook event, or Peen event type, and extends `make lint` to the build-tagged test suites.

## v0.18.2 (2026-10-10)

Peen now exits when it cannot start instead of hanging. No configuration or API migration is needed.

- Fixes Peen staying up forever after a startup failure, such as an invalid `PEEN_LOG_RETENTION` or an unknown name in `PEEN_DISABLED_CORE_SKILLS`. It logged the error but never exited and never became ready, so a container restart policy never kicked in. Peen now exits with a non-zero status. Peen updates its Servicepack framework to v1.10.0, which carries the fix.
- Changes the development CPU cap to come from Servicepack v1.10.0 instead of Peen's own Makefile. `DEV_CPUS` works as before, and `DEV_CPU_LIMIT_ARGS=` turns the cap off.

## v0.18.1 (2026-10-10)

`run_command` no longer loses the output of commands that finish quickly, and several v0.18.0 skill, event, and control surface problems are fixed. No configuration or API migration is needed.

- Fixes `run_command` and background jobs often returning empty output for a command that exited quickly, such as `echo hello`. The output was printed but discarded before Peen read it. Peen now uses commander v0.6.0, which keeps a command's output until it has been read, waiting up to 2 seconds after the command exits, and attaches to the output before the command starts.
- Fixes, under `PEEN_COMPACTION_MODE=summarize`, a skill loaded with `use_skill` in an earlier turn being sent again in full when a later message names it with `:name`. It now gets the short note that the skill is already loaded, the same as a skill delivered through `:name`, until a compaction covers it.
- Fixes, under the default `drop-oldest` compaction mode, a skill named with `:name` in an earlier turn being answered with the "already loaded" note after the request had left the skill message out, so the model had neither the skill nor a summary of it. A skill now counts as already loaded only in the turn that delivered it, by `:name` or `use_skill`, so a `:name` in a later turn delivers it in full again.
- Fixes a skill summarized away by a compaction during a running turn being answered with the "already loaded" note when a later queued message names it. It is now delivered in full again.
- Fixes every turn that started with pending session events storing its `<events>` message before the prompt, or before the `<handler>` message of a turn an event handler started, the reverse of the order the model received them, so a later turn replayed them in the wrong order. Peen now stores the prompt, its skill messages, then the events, and `GET /v1/messages` returns them in that order. Messages stored by earlier versions keep their order.
- Fixes the control surface showing a delivered `<events>` block as raw tags and JSON. It now shows "Session events" and one `type: summary` line per event.
- Fixes `wait_job` accepting an invalid `timeout` without an error when the job ID is unknown. The timeout is now checked first.
- Fixes every successful child agent launch logging a spurious "Trying to wrap a nil error" error.
- Adds tests for every v0.18.0 change, including API tests that run through the session worker, startup tests against the real binary, and real-browser tests for the skill marker and event handler updates.
- Changes the development Make targets to run their dev containers with `--cpus` set to all but two CPU threads. The cap is at least one CPU, and `make run-dev` is not capped. Set `DEV_CPUS` to change it. Adds `make test-stress`, which runs the fast command and job tests 200 times with `GOMAXPROCS` set to 1 and then 4.

## v0.18.0 (2026-10-10)

A skill named with `:name` now reaches the model as its own message, at the start of a turn and in a message sent while a turn runs, and skills can name other skills. Peen's embedded skills and agent become core skills and a core agent that sit next to your own instead of being replaced by them, and every time value Peen accepts is a Go duration. Migrate before you upgrade: rename `PEEN_LOG_RETENTION_DAYS`, the `timeoutSeconds` tool arguments, and the hooks `timeout_seconds` field, and set `PEEN_AGENT` to `core:general-purpose` or drop it.

- Breaking: a skill named with `:name` is no longer added to the system prompt. Each named skill is sent as its own user message right after the message that named it, wrapped in `<skill>` with its name, description, and directory, at the start of a turn and in a message queued during a running turn. The stored transcript keeps your message as typed and stores each skill message as injected with `injectedKind: "skill"`, and every client receives a `skill.injected` event. A skill named again later in the conversation gets a short note instead of a second copy. An unknown `:name` fails the message with `VALIDATION_FAILED` and a message that names the skill and lists up to 10 of the available ones. The default system prompt now tells the model how to treat `<skill>` messages.
- Breaking: a skill the user named on the parent turn no longer reaches every child agent. A `:name` in the `task` given to `launch_agent` is handled like a user message: the skill follows the task in the child's first request, and an unknown name fails the call.
- Breaking: Peen's embedded skills and agent are now core skills and a core agent, addressed as `core:<name>`. A skill or agent of yours with the same name no longer replaces the core one; both exist. A plain name picks yours first and the core one second, so `:planning` is your `planning` skill when you have one and `:core:planning` is always Peen's. Catalogues list core entries first. If you replaced a core skill with your own, yours still answers to the plain name; set `PEEN_DISABLED_CORE_SKILLS` to that name to drop the core copy.
- Breaking: the embedded `default` agent is renamed `general-purpose` and `PEEN_AGENT` defaults to `core:general-purpose`. Startup fails when `PEEN_AGENT=default` and no `default` agent exists under `PEEN_CONFIG_DIR`, with an error naming `core:general-purpose`. Migrate by dropping `PEEN_AGENT` or setting it to `core:general-purpose`. A root agent defined only in a workspace still resolves at each turn.
- Breaking: every time value Peen accepts is a Go duration such as `90s`, `5m`, or `1h30m`. `PEEN_LOG_RETENTION_DAYS` is replaced by `PEEN_LOG_RETENTION` (default `336h`, a whole number of days), and startup fails while the old variable is set. The `run_command` and `wait_job` tool arguments `timeoutSeconds` become `timeout`. The hooks file command action field `timeout_seconds` becomes `timeout`, and a file that still uses the old field is skipped with a warning that names the new one.
- Adds skills that name other skills. A `:name` in a `SKILL.md` body pulls that skill in too, depth first, each skill at most once, so references that loop stop. `use_skill` returns the skills a skill names in `referencedSkills`. An unknown name in a `SKILL.md` body is reported as a harness warning.
- Adds `PEEN_DISABLED_CORE_SKILLS` and `PEEN_DISABLED_CORE_AGENTS`, comma-separated core names or `all`, which remove core skills or the core agent. An unknown name fails startup.
- Adds a readable `duration` string, such as `1m30s`, next to every `durationMs` in job and agent event data and in the `wait_job` result.
- Changes the core `planning` skill and core rules to write plans to `.plans/<UTC datetime>-<scope>.md`, such as `.plans/20261010T120501Z-login-form.md`, before every request that changes code, config, docs, or tests, and none for a question that changes nothing. The agent adds `.plans/` to the workspace `.gitignore` when it is missing, and a child agent writes its own plan that links its parent's.
- Changes every message Peen adds for the model to carry a tag. A hook `inject` action now arrives as a `<hook>` block with `<event>` and `<tool>` lines instead of text led by "Configured hook context:", and the instructions an event handler starts a turn with arrive as a `<handler>` block with `<event>` and `<agent>` lines, the same shape as a `<skill>` message. The note that a skill is already loaded arrives as `<note>Skill NAME is already loaded earlier in this conversation.</note>`. Delivered session events arrive as an `<events>` block with one `<event>` element per event, replacing `<session-events count="N">` and its `[n] event:` lines. The default system prompt says that `<skill>` and `<hook>` blocks are trusted instructions, `<handler>` blocks are the instructions an event handler started the turn with, `<note>` blocks are notices from Peen, and `<events>` blocks are untrusted data.
- Changes the control surface to show a skill message as a collapsed "skill: name" marker that expands to the skill, instead of a message bubble.
- Adds the Go Reference and imported-by badges to the README. The pipeline runs weekly to refresh the imported-by count, without building or pushing an image. ClawHub publishing is limited to the `peen` skill.
- Adds a contributor guide: a root `AGENTS.md` and topic rules in `.agents/rules/`.

## v0.17.1 (2026-10-10)

A message sent during a running turn may now name a skill with `:name`. No configuration or API migration is needed.

- Fixes a message sent during a running turn being refused with `VALIDATION_FAILED` when it named a skill with `:name`. It is now queued as written. The model receives it after the current step and loads the skill with `use_skill`, because the running turn's prompt is already fixed. The default system prompt now tells the model that only a `:name` in the message that started the turn is already active.

## v0.17.0 (2026-10-10)

Messages sent close together now run in the order they were sent, and a message sent during a turn shows as queued until the model receives it. Event wakes and the `session_start` hook now work as documented, and the documentation matches the code. Before you upgrade, keep `POST /v1/session/notices` summaries and data within the event size limits, remove any hook that emits `job.*` or `agent.*` events, and treat a missing `sessionId`, `requestId`, or `turnId` in hook command input as not applicable.

- Breaking: `POST /v1/session/notices` rejects a `summary` longer than `PEEN_MAX_EVENT_SUMMARY_BYTES` or `data` larger than `PEEN_MAX_EVENT_DATA_BYTES` with a 400 validation error. It used to accept the event with the summary cut short and the data silently dropped. A hook `emit_event` action or command-output event over either limit now fails the action. Events Peen produces itself are still shortened, now without splitting a UTF-8 character, and a dropped `data` object is logged.
- Breaking: hooks can no longer publish `job.*` or `agent.*` events, which are reserved for Peen. A hooks file with an `emit_event` action using a reserved `event_type` is invalid, and a command-output event with a reserved `type` fails the action. Before, only `POST /v1/session/notices` refused them, so a hook could forge a `job.exited` event.
- Breaking: hook command input leaves out `sessionId`, `requestId`, and `turnId` when they do not apply, such as `turnId` on `pre_user_message`. They used to arrive as all-zero UUIDs.
- Fixes an event's own `delivery: wake` never starting a turn. A handler file in `.agents/events/` that leaves out `delivery` was read as `delivery: queue`, so only a handler that said `delivery: wake` could wake a session. A handler without `delivery` now leaves the decision to the event, as documented.
- Fixes the `session_start` hook never running when a client opens a session through the controller. It ran only when the same process created the session, and the session worker always resumes a session the controller created. It now runs on the first turn a session ever runs.
- Fixes messages sent close together, from one client or several, being run out of order or refused. Two messages sent while a session's worker was still starting could both be dispatched at once: the later one could start the turn, the earlier one was refused as `SESSION_BUSY`, and two workers could be launched for one session. Peen now admits a session's messages in the order it receives them, so the first starts the turn and the rest queue.
- A message sent during a running turn may now repeat that turn's `model` and `reasoningEffort`, which is what the control surface sends. Changing them, setting a `systemPrompt`, or activating a skill with `:name` is still refused, now with `VALIDATION_FAILED` and a message that says why, instead of `SESSION_BUSY`. A full queue now answers `USER_MESSAGE_QUEUE_FULL` instead of `SESSION_BUSY` when the session runs in a worker.
- Adds a `user_message.delivered` WebSocket event. A message sent while a turn runs is queued, and when the running turn's current step ends, the message reaches the model and every client receives `user_message.delivered` with that message's `requestId`. Several queued messages are delivered in the order they were sent.
- Shows a message sent during a running turn in the control surface as queued, with "Queued. Lands after the current step.", until it reaches the model. It then appears inside the running turn at the point the model received it.
- Corrects the documentation against the code. Among the fixes: the agent skill's Docker quick start now starts, its WebSocket example opens a session instead of inventing an ID, `emit_event` explains `type` versus `event_type`, the sandbox Docker profile example allows the network the worker needs to reach the model provider, `docs/http-api.md` documents `POST /v1/sessions/open`, `GET /v1/sessions`, and every protocol event type, and `docs/architecture.md` gains a terms and identifiers section.
- Changes the control surface chat layout. The agent's output runs across the full width with no speech bubble or speaker label, only your own messages are bubbles, on the right, and the message box spans the full width. Enter sends a message and Shift+Enter starts a new line.
- Updates the Servicepack framework files to v1.9.4, which build with Go 1.26.9.

## v0.16.1 (2026-10-10)

Rebuilds Peen with Go 1.26.9 to fix reachable standard library vulnerabilities, and stops `run_command` from losing stderr. No configuration or API migration is needed.

- Builds with Go 1.26.9, which fixes twelve standard library vulnerabilities that `govulncheck` reports as reachable from Peen: HTTP/2 server crashes and memory and CPU exhaustion, HTTP/1 connection desynchronization after `CONNECT`, unbounded `Range` and MIME header parsing in `net/http`, `net/textproto`, and `mime/multipart`, malformed ECH handling in `crypto/tls`, and escaping bugs in `html/template` (GO-2026-6599, GO-2026-6600, GO-2026-6603, GO-2026-6605, GO-2026-6607 through GO-2026-6613, and GO-2026-6617). Images up to v0.16.0 were built with Go 1.26.6. `go.mod` now requires Go 1.26.9.
- Fixes `run_command` and background jobs sometimes losing a command's stderr when the command wrote to stdout and exited quickly, for example `printf 'ok\n'; echo failed >&2`. The stderr lines still queued when stdout closed were dropped. Peen now uses commander v0.5.9, which delivers both streams until each one closes.

## v0.16.0 (2026-10-09)

Hooks from every layer now always run, and tool cards in the control surface show what each call acted on. Remove `PEEN_ENABLE_WORKSPACE_HOOKS` from your configuration before you upgrade.

- Breaking: removes `PEEN_ENABLE_WORKSPACE_HOOKS`. Hooks from every layer now run, the workspace's included, and a workspace that should run no hooks has no `.agents/hooks.yaml`. Peen refuses to start while the variable is set to anything other than `true`, because a deployment that set it to `false` relied on it to keep workspace hooks from running. Migrate by removing the variable, and delete `.agents/hooks.yaml` from any workspace whose hooks must not run. The Go `pkg/peen` option `EnableWorkspaceHooks` is removed too.
- Shows what each tool call acted on next to its name in the control surface, such as `use_skill: release-notes`, `run_command: make test`, or `read_file: README.md`, so a card says what happened without being opened.

## v0.15.2 (2026-10-09)

Session events now wake a session after its first turn, so a `job.exited` handler can react when an app the agent started crashes. No configuration or API migration is needed.

- Fixes session events never waking a session after its first turn when the session runs in a worker, which is every session a controller serves. The worker kept counting its finished turn as running, so every wake fell back to a queued delivery. A `job.exited` handler with `delivery: wake`, for example, never ran when the app a background job started crashed after its turn.

## v0.15.1 (2026-10-09)

A turn that a session event wakes now runs in the session's worker and shows up live in the chat. No configuration or API migration is needed.

- Fixes a turn that a session event woke running inside the controller instead of the session's worker. It ignored the session's execution profile, so a session on a Docker profile ran its woken turn outside the container, and none of the turn reached WebSocket clients, so the chat showed nothing until the page reloaded. A woken turn now goes to the session's worker like a turn a client sends, and streams live.

## v0.15.0 (2026-10-09)

`AGENTS.md` now follows the [agents.md](https://agents.md) format: a file in a subdirectory of the workspace covers that directory, and the closest one wins. No configuration or API migration is needed. A workspace that has `AGENTS.md` files in its subdirectories now sends them to the model.

- Reads `AGENTS.md` files below the workspace at the start of every turn, after every layer, parents before children. Each one starts with a line naming the directory it covers. Peen skips hidden directories, `node_modules`, `vendor`, symlinked directories, and a config directory inside the workspace, and searches at most 10,000 directories. A nested file that is empty, unreadable, or past a harness limit is skipped with a warning instead of failing the turn.
- Tells the model, in Peen's own operating instructions, that the `AGENTS.md` closest to the file being changed wins and that the user's chat messages override every `AGENTS.md`.
- Documents `AGENTS.md` as the project's instructions file and rules as separate topic files. `docs/rules.md` is now "AGENTS.md and rules", and the README, harness guide, getting-started guide, and architecture overview use the same split.

## v0.14.2 (2026-10-09)

Fast commands keep their output, and a session's first turn no longer fails at random while its worker starts. No configuration or API migration is needed.

- Fixes `run_command` losing all output from a command that finished before Peen recorded its start, which a worker does over its socket. A quick `echo` came back with empty `stdout`, and a failure such as exit code 126 came back with empty `stderr`. Peen now reads output from the moment the process starts and records it once the start is recorded.
- Fixes a session's first turn sometimes failing with "the worker disconnected before it could run work". The controller treated a worker as ready once it approved the registration, before it recorded the connection, so a launch that looked for the worker in that gap failed.

## v0.14.1 (2026-10-09)

Hook input conditions work again, commands stop reporting their result twice, and the control surface groups a conversation by turn. No configuration or API migration is needed. A handler that listened for `hook.action_failed` never fired, so point it at `hook.action.failed`.

- Fixes hook `input` conditions being dropped when Peen loaded the harness, so a group or action that matched on tool input, such as `match.input./command.regex`, ran on every call of that tool. A `deny` guarding one dangerous command blocked every command instead.
- Fixes every `run_command` call also publishing a `job.exited` session event, even when the command finished inside the call. The model got each result twice, once as the tool result and once as a background update, and the chat showed a background update after every command. Only a command that is still running when its call returns, because it ran in the background or outlived its timeout, now publishes a completion event.
- Fixes `emit_event` hook actions without `data`, and hook commands that print `"data": null`, failing instead of publishing an event with no payload.
- Fixes the hook failure event never being published. Its type, `hook.action_failed`, broke the event type rules, so every publish was rejected. It is now `hook.action.failed`.
- Adds `turnId` to stored messages, and the control surface now groups a conversation by turn. A background update delivered at the start of a turn shows at the start of that turn's reply instead of at the end of the previous one, and a turn an event handler started gets its own reply instead of running into the one before it.
- Marks the instructions an event handler starts a turn with as `injected`, so the chat shows them as a background update instead of as a message you typed.
- Documents in `docs/hooks.md` that the agent can edit or delete workspace hook files with its own tools, so a guard you rely on belongs in `PEEN_CONFIG_DIR` with sessions on a Docker profile.

## v0.14.0 (2026-10-08)

The control surface now streams replies as they are written, renders them as Markdown, shows each tool call as one card, and lets you pick the model and reasoning level for each turn. A controller has one workspace root, and every project inside it opens as its own workspace. Deployments that set `PEEN_WORKSPACE_ROOTS` must switch to `PEEN_WORKSPACE_ROOT`.

- Breaking: `PEEN_WORKSPACE_ROOTS` is replaced by `PEEN_WORKSPACE_ROOT`, one absolute directory. Peen refuses to start while the old variable is set, and the error names the new one. `GET /v1/workspace-roots` keeps its shape and returns that single root. Migrate by setting `PEEN_WORKSPACE_ROOT` to the directory that holds your projects, or leave it unset to keep using the working directory.
- Fixes the chat showing one row per streamed `content_block_delta` while the reply itself never appeared until the turn ended. The page now folds content blocks into one live reply: text and reasoning stream in, and each tool call is one card paired with its result.
- Renders assistant replies as GitHub-flavored Markdown. Raw HTML in model output is not rendered, links are limited to http, https, mailto, and relative targets, and images are shown as links instead of being loaded.
- Keeps the conversation scrolled to the newest output until you scroll up, and resumes following when you scroll back to the bottom.
- Loads the newest 100 messages of a session instead of the oldest 100, so long sessions show their latest work.
- Adds `reasoningEffort` to `message.send`: `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`, for that turn only. Peen fits the level to the turn's model, so a model without reasoning levels runs without one and a level past the model's range runs at its nearest supported level. A message queued into a running turn cannot set it.
- Adds a Reasoning picker next to Model in the control surface. The Model picker now starts on the session's own model instead of "Session default", and both pickers lock while a turn runs because queued messages keep the running turn's settings.
- Shows each tool call in a stored conversation as one card with its status (running, done, error, or no result). Expanding the card shows its arguments and its result or error, and thinking blocks expand the same way.
- Adds `injected` to stored messages. Messages Peen adds itself, such as delivered session events, are marked and shown as a collapsed background update instead of as your own message.
- Fixes a user's message not appearing live in worker mode. The events a turn opens with are now published to WebSocket clients after they are written, like every checkpointed event.
- Fixes the session list's message count staying stale until a manual refresh.
- Fixes the Docker image's entrypoint, which ran `./app` relative to the working directory and failed whenever the container started outside `/app`, including the documented `-w` workspace and every Docker worker.
- Fixes a path cleaning issue in the embedded web server that the security scan flagged, and builds integration test images with Docker's classic builder.
- Documents the live event protocol in `docs/http-api.md`, including how a client folds content blocks into a reply.
- Adds a guide for each part of the harness: `docs/harness.md` for the layer order, load timing, prompt order, and limits, plus `docs/rules.md`, `docs/skills.md`, `docs/agents.md`, and `docs/events.md`. `docs/hooks.md` now covers all 38 events with their input, every action, the command protocol, failure handling, and an example for each kind of hook. The README gives a short overview of each and links to its guide.
- Corrects the docs on rule layering. Peen reads rule files from the configuration directory and from every directory between `/` and the workspace. It never reads below the workspace and never loads a rule because the model touched a file. Also corrects that `emit_event` `delivery` defaults to `queue`, that an event handler's `agent` field only adds a hint to the wake message, and that `PEEN_MAX_PENDING_EVENTS` never drops what the model receives.
- Fixes `make web-pkg-add`, `web-pkg-update`, and `web-pkg-remove`, which never passed `WEB_PKG` into the build container.

## v0.13.1 (2026-10-07)

A hook that denies a tool now hands the model its reason exactly as written.
No configuration or API migration is needed.

- Fixes deny reasons that reached the model cut short. Peen recovered the
  reason by parsing the error text and stopped at the first ` [`, which is
  where an error's source location begins. A reason containing Markdown
  links, checkboxes, or Go types such as `[]string` lost everything after
  that point and carried an internal `run hook action:` prefix. Both the
  `deny` action and a command's `deny` decision now pass the reason through
  unchanged.
- Documents how to give the model a project rule the first time it changes a
  matching file, using one hooks file and two short scripts. The recipe in
  `docs/hooks.md` delivers the rule once per conversation, keeps a separate
  delivery for each child agent, and delivers it again after a compaction.

## v0.13.0 (2026-09-24)

Peen now has a complete workspace-first controller flow. The bundled browser
client opens or resumes a workspace session, shows live work as it happens,
and gives clear feedback when a workspace or harness definition needs repair.

- Breaking: `message.send.data.workdir` is no longer accepted. Open or resume
  the workspace first with `POST /v1/sessions/open`, then send the returned
  session ID in WebSocket event metadata. This prevents individual messages
  from changing the directory a durable session owns.
- Adds workspace-root discovery and a typed workspace-opening API. The
  controller rejects missing, non-directory, or out-of-root paths with a
  specific client-safe error instead of a generic server failure.
- Reworks the bundled control surface into a full-height coding chat. It can
  open an existing child workspace, switch sessions, show thinking, tool calls,
  output, failures, profiles, and the complete durable session record.
- Invalid optional `AGENTS.md`, rules, skills, named agents, event handlers,
  and hooks no longer prevent a usable workspace from running. Peen logs and
  stores a durable `harness.warning` event with the skipped source and repair
  reason, while valid sibling configuration still loads. Unreadable workspaces
  and resource limits remain hard failures.
- Adds `make run-dev` for a local controller using the current host UID and
  GID. It uses the invoking checkout as its workspace and does not grant
  Docker socket access unless explicitly requested.
- Removes the unused hello-world example service and keeps coverage output
  scoped to Peen's actual packages.

## v0.12.0 (2026-09-23)

Peen now includes a browser control surface at the controller root URL. It
connects to the same global WebSocket feed as every other client, and it reads
durable state through the public API. No database, configuration, or API
migration is needed.

- Embeds a static Svelte control surface in the Peen binary and production
  image. It opens or resumes workspace sessions, shows their durable records,
  sends WebSocket turns, cancels running work, selects profiles, and keeps the
  active session view in step with global live events.
- Adds `GET /v1/models`, which lists the non-secret models confirmed when the
  controller started. Each result includes the exact `connection/model` name
  a client may send as a one-turn model override, its connection, raw model ID,
  and enforced context window.
- Adds the frontend build, typecheck, lint, unit-test, and real-browser checks
  to the project tooling and CI. Browser diagnostics log only a closed set of
  safe metadata and never API tokens, message text, or event payloads.
- Normalizes generated control-surface text assets before they enter the
  embedded asset tree, so the release whitespace check accepts every fresh
  frontend build.

## v0.11.0 (2026-09-23)

Child agents now keep their own durable conversations and compaction lineages.
Existing databases migrate automatically. No API migration is needed. Hook
commands must use the `stateDirectory` in their invocation instead of assuming
that hook state lives below `PEEN_CONFIG_DIR`.

- Stores each child task, assistant message, tool result, and hook injection in
  SQLite before the child continues. A child summary stays separate from the
  parent session, retains immutable direct message membership, and links to the
  summary it supersedes.
- Adds `GET /v1/session/agents/{agentRunId}/messages`,
  `GET /v1/session/agents/{agentRunId}/compactions`, and
  `GET /v1/session/agents/{agentRunId}/compactions/{compactionId}`. They expose
  a child transcript and compaction lineage only to its owning session.
- Adds `.claude/rules/*.md`, `.agents/rules/*.md`, and compatible
  `.claude/skills/` discovery. A standalone `:skill-name` in a user message
  activates that resolved skill before the first provider request, including
  for child agents.
- Raises the default child-agent depth to five. The child at the limit cannot
  launch another child. Child prompts now include Peen's base instructions, and
  child hook command input includes `agentRunId`.
- Keeps writable hook state below `PEEN_STATE_DIR` for native workers and in a
  Docker worker's session-private writable mount. The configuration directory
  stays read-only for every Docker worker.
- Requires session-notice and hook-emitted `data` to be a JSON object before a
  durable write. Peen renders delivered notices as quoted JSON data, so event
  text cannot alter the prompt boundary.
- Lets an operator select any Docker image reference the daemon can resolve,
  including a local development image. Worker records keep a repository digest
  when the daemon supplies one.

## v0.10.1 (2026-09-21)

Maintenance release. No configuration, API, or workspace migration is needed.

- Updates the Servicepack baseline to v1.9.3, including its current security
  maintenance.
- Keeps Peen's dependency policy and byte-exact test fixtures project-owned, so
  future framework updates do not replace them.
- Makes the command process-group test wait for the forked process PID to be
  written before stopping it. This removes a timing-dependent CI failure.

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
