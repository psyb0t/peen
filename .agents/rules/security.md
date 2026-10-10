# Security

Peen runs model-chosen commands with real access to a workspace, so its own boundaries carry the weight. A change that touches one of the boundaries below keeps it intact and has a test that proves it.

## Secrets

- Never commit a real secret. `.env` and `.env.*` are gitignored. `.env.example` holds only obviously fake values. `.gitleaks.toml` allowlists by path only, and only paths that are not tracked.
- Provider keys stay in their own environment variables, named by `apiKeyEnv`. Peen's own code never writes them into `PEEN_UPSTREAMS`, SQLite, events, logs, audit records, error messages, or API responses. Model call records keep the connection name, not the key.
- A worker's credential is generated per worker generation and handed over on stdin. The controller stores only its SHA-256 hash. It never goes on a command line, into an environment variable, a container label, or a log.
- Compare tokens, credentials, and hashes with `subtle.ConstantTimeCompare`, never `==`.
- Generate tokens and credentials with `crypto/rand`.

## Directories and workspaces

- `PEEN_CONFIG_DIR` is read by every worker, read-only. Put nothing there that one session must not see.
- `PEEN_STATE_DIR` holds SQLite, audit logs, and worker sockets. No worker ever receives it, so one session cannot read another's transcript. Startup refuses a state directory equal to, inside, or containing the config directory, after resolving symlinks.
- A requested workspace is resolved through its symlinks and must sit inside `PEEN_WORKSPACE_ROOT`. Anything outside answers `WORKSPACE_NOT_ALLOWED` and creates nothing.
- The file tools and `run_command` deliberately have the full access of the account the worker runs as. There is no path allowlist and no approval step, and the docs say so. The execution profile is the isolation boundary: a `docker` profile limits a worker to the mounts, network, and capabilities the operator defined. Do not describe a tool as confined when it is not, and do not let a client request choose an image, mount, network, or capability.

## Workers and containers

- Each session's worker gets its own socket directory and nothing belonging to another session.
- Docker workers receive no Docker socket, no state directory, no control API token, and only the environment on the allowlist in `internal/pkg/config/workerenvironment.go` plus the named provider credentials their model calls need.
- The worker entrypoint starts as root only to create the controller's host account, then drops to it. The agent never runs as root. `sudo` is granted only when the execution profile sets `allowPrivilegeEscalation`.
- The controller gets a Docker socket only when the operator configures one for Docker profiles. It is host-root-equivalent, so no other code path may grow a need for it.

## Hooks and the harness

Hooks from every layer run with Peen's own access, the workspace's included, and the docs tell operators to open only workspaces whose hooks they trust. Hook files are validated strictly, and hooks cannot publish the reserved `job.*` and `agent.*` event types. Keep both rules. A hook or event handler never receives a credential it was not configured with.

## Untrusted input

- Every `/v1` request is validated against `api/api.yml` before a handler runs, behind authentication and a body size limit. A new endpoint inherits this by living in the spec.
- Bind requests to the generated types. Never decode a body straight into a database model.
- Start processes with an argument list (`exec.CommandContext(ctx, name, args...)`). Only `run_command` runs a shell, because running the model's command is its job. Nothing else builds a shell string from request, config, or model data.
- SQL goes through gorm/gen with bound parameters. See `database.md`.
- Error responses to clients carry no source paths or internal detail.
- The control surface never renders untrusted HTML. See `web.md`.

## Transcripts

Peen records tool calls and results verbatim and sends them over the WebSocket feed. It does not scan tool output for secrets. A feature that copies transcripts somewhere new says so in the docs, and no doc claims redaction that does not exist.
