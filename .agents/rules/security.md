# Security

Peen runs model-chosen commands with real access to a workspace, so its own boundaries carry the weight. A change that touches one of the boundaries below keeps it intact and has a test that proves it.

## Secrets

- Never commit a real secret. `.env` and `.env.*` are gitignored. `.env.example` holds only obviously fake values. `.gitleaks.toml` allowlists by path only, and only paths that are not tracked.
- Provider keys stay in their own environment variables, named by `apiKeyEnv`. Peen's own code never writes them into `PEEN_UPSTREAMS`, SQLite, events, logs, audit records, error messages, or API responses. Model call records keep the connection name, not the key.
- A worker's metrics reports are untrusted input. `metrics.WorkerIngest` stamps the identity the connection authenticated, refuses oversize reports, accepts only the kinds a worker may report, and caps label values. Turns, child agents, compactions, and worker lifecycles are counted from the controller's own writes, never from a worker's report. A worker's metrics query carries no session: `WorkerIngest.Summarize` reads the session the connection authenticated, through a closed set of summaries with no SQL from the caller.
- A worker's credential is generated per worker generation and handed over on stdin. The controller stores only its SHA-256 hash. It never goes on a command line, into an environment variable, a container label, or a log.
- Compare tokens, credentials, and hashes with `subtle.ConstantTimeCompare`, never `==`.
- Generate tokens and credentials with `crypto/rand`.

## Directories and workspaces

- `PEEN_CONFIG_DIR` is read by every Docker worker, read-only unless the operator sets `PEEN_WRITABLE_CONFIG_DIR`. Put nothing there that one session must not see. A `control` worker runs as the controller's user and can write it. With `PEEN_AUTO_EVOLVE`, Docker workers also get the global learned directory (`PEEN_LEARNED_DIR`), read-only unless the operator sets `PEEN_WRITABLE_LEARNED_DIR` or `PEEN_WRITABLE_CONFIG_DIR`, because every session loads it. Startup refuses one that is or contains the config directory or overlaps the state directory, the worker socket root, or the audit log directory. `internal/pkg/config/learneddirectory_test.go` holds that rule.
- `PEEN_STATE_DIR` holds SQLite, audit logs, and worker sockets. No Docker worker receives it, so one Docker session cannot read another's transcript. A `control` worker runs as the controller's user and can read it, and the docs say so. Startup refuses a state directory equal to, inside, or containing the config directory, after resolving symlinks.
- A requested workspace is resolved through its symlinks and must sit inside `PEEN_WORKSPACE_ROOT`. Anything outside answers `WORKSPACE_NOT_ALLOWED` and creates nothing.
- A workspace may not be, contain, or sit inside the state directory, the worker socket root, or the audit log directory, and may not be or contain the config directory, because a Docker worker mounts its workspace writable. The refusal is the same `WORKSPACE_NOT_ALLOWED` and names no directory. `internal/pkg/control/workspace_test.go` and `tests/api/workspacepolicy_test.go` hold that rule.
- The `peen` command (`scripts/peen.sh`) stops or removes only a container whose `peen.install` label names its own configuration directory, never passes `--privileged`, never sources `.env`, and starts a host controller with a minimal environment. `tests/installer` proves each of these.
- The file tools and `runCommand` deliberately have the full access of the account the worker runs as. There is no path allowlist and no approval step, and the docs say so. The agent space is the isolation boundary: a Docker profile limits a worker to the mounts, network, and grants the operator defined, and the `control` agent space has no boundary at all. Do not describe a tool as confined when it is not, and do not let a client request choose an image, mount, network, or capability.
- When the controller can reach a Docker daemon, startup refuses the `control` agent space as the default and never offers it, because a control worker has the controller's user and groups and can use the daemon to start a container that mounts the host. Every candidate counts: `PEEN_DOCKER_SOCKET`, a `unix://` `DOCKER_HOST`, the default socket, and a `tcp://` `DOCKER_HOST`. Keep that rule and its tests (`internal/pkg/config/agentspaces_test.go`, `internal/pkg/config/dockerauthority_test.go`) when touching agent space selection.
- Commands the agent runs get the worker's environment with `PEEN_API_TOKEN` and every `apiKeyEnv` variable removed (`internal/pkg/config/commandenvironment.go`). A new setting that carries a secret goes into `secretSettingKeys`.
- A `control` worker gets the controller's environment without the controller-only keys in `workerEnvironmentReserved` and without any `PEEN_` key outside the worker allowlist, except the provider credentials its model calls need (`ControlWorkerEnvironment` in `internal/pkg/config/workerenvironment.go`). A new controller-only setting goes into `workerEnvironmentReserved`.
- The system agent is the one exception to the rule that `control` is never offered beside a Docker daemon. With `PEEN_SYSTEM_AGENT=true`, the system session runs in `control` even when a daemon is reachable, and that makes it host root. Startup warns, and the setting stays off by default. Keep every other session refused from `control` in that case. The boundary holds only because the session cannot be reached any other way: its root agent `core:system` is written only by `Registry.OpenSystemSession`, the name is reserved in harness files and `PEEN_AGENT`, the agent is in no snapshot but its own, a client must send `message.send` with `system` set, and the server never reads a command out of message text. The supervisor decides from the session row at every launch. Its worker loads no harness: its configuration directory is its own empty workspace, because a Docker session can write the real one under `PEEN_WRITABLE_CONFIG_DIR`, and a planted `SYSTEM.md` or hook would run with this authority. `internal/pkg/harness/systemagent_test.go`, `internal/pkg/worker/run/systemworker_test.go`, `internal/pkg/worker/supervisor/systemagent_test.go`, and `tests/api/systemagent_test.go` hold these rules.
- The controller (`control-core`) and `peen worker` call `nondumpable.Enable` before they do anything else, and a failure stops startup. That keeps a same-user agent out of their `/proc/<pid>/environ` and memory. It does not stop root or `CAP_SYS_PTRACE`, and it does not cover `PEEN_STATE_DIR`, which a `control` agent can still read and change. Keep the call first in both paths and keep `internal/pkg/nondumpable` and `tests/api` proving it.

## Workers and containers

- Each session's worker gets its own socket directory and nothing belonging to another session.
- Docker workers receive no Docker socket, no state directory, no control API token, and only the environment on the allowlist in `internal/pkg/config/workerenvironment.go` plus the named provider credentials their model calls need.
- The worker entrypoint starts as root only to create the controller's host account, then drops to it. The agent never runs as root. `sudo` is granted only when the Docker profile sets `sudo: true`. A worker gets the controller's Docker socket, mounted at `/var/run/docker.sock`, only when its profile sets `docker: full`, which is host root and is flagged as a capability warning. Startup refuses `docker: full` when the socket's group is 0. A `docker: readonly` worker gets no socket: it reaches the proxy in `internal/pkg/worker/dockerproxy` through its own session socket directory. The proxy forwards only allowlisted `GET` and `HEAD` endpoints, refuses unclean or encoded paths and connection upgrades, and redacts `Config.Env` in inspect output. Widening the allowlist is a security change with a test for each endpoint.
- The controller gets a Docker socket only when the operator configures one for Docker profiles. It is host-root-equivalent, so no other code path may grow a need for it.

## Hooks and the harness

Hooks from every layer run in the session's worker with the agent's access, the workspace's included, and the docs tell operators to open only workspaces whose hooks they trust. Hook files are validated strictly, and hooks cannot publish the reserved `job.*`, `agent.*`, and `turn.*` event types. Keep both rules. A hook or event handler never receives a credential it was not configured with.

## Untrusted input

- Every `/api/v1` request is validated against `api/api.yml` before a handler runs, behind authentication and a body size limit. A new endpoint inherits this by living in the spec.
- Bind requests to the generated types. Never decode a body straight into a database model.
- Start processes with an argument list (`exec.CommandContext(ctx, name, args...)`). Only `runCommand` runs a shell, because running the model's command is its job. Nothing else builds a shell string from request, config, or model data.
- SQL goes through gorm/gen with bound parameters. See `database.md`.
- Error responses to clients carry no source paths or internal detail.
- The control surface never renders untrusted HTML. See `web.md`.

## Transcripts

Peen records tool calls and results verbatim and sends them over the WebSocket feed. It does not scan tool output for secrets. A feature that copies transcripts somewhere new says so in the docs, and no doc claims redaction that does not exist.
