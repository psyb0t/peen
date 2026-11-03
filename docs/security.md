# Security

Read this before you deploy Peen anywhere it can reach something you do not want touched.

## What the agent can do

A session's tools run in a worker process in the agent space the operator picked for that session. In the `control` agent space that worker is a child of the controller, so `runCommand` and the file tools have the same access as the operating-system user running Peen. There is no path allowlist, no secret-file denylist, and no approval or permission step before a tool runs. A file tool can read, write, or remove any path that user can touch, including `.git/`, `.env`, and SSH keys. `runCommand` runs whatever shell command the model gives it, at once. Peen is a coding agent with real access, and this is how it gets that access. The active workspace is a default, not a boundary, and an absolute tool path can still point outside it.

`removePath` has one built-in restriction, a guard against a typo rather than a permission system. It refuses to remove the filesystem root, the session's original workspace directory, or the agent's current directory. Every other path, including everything named above, can be removed.

## Credentials

Commands the agent runs start with an environment that has `PEEN_API_TOKEN` and every provider `apiKeyEnv` variable removed. On Linux the controller and its workers are non-dumpable, so a command cannot read those values from their `/proc/<pid>/environ` or memory. A `control` agent still runs as the controller's user, so it can read and change `PEEN_STATE_DIR`, which holds every session's transcript, and it can read any file or other process of that user that holds a secret. Root and processes with `CAP_SYS_PTRACE` can still read a non-dumpable process. See [Where it runs](where-it-runs.md#agent-spaces) for the full list.

## Docker agent spaces

A `docker:<profile>` agent space is the isolation boundary. It runs the worker in its own container with only the mounts, network, and grants the operator defined. A client picks an agent space by name and never sends an image, mount, network setting, or capability, so how much an agent can reach is the operator's decision. If the controller cannot reach a Docker daemon, a session in a Docker agent space is refused rather than run on the host. If it can, Peen refuses the `control` agent space, because a reachable daemon makes `control` sessions host root. See [Where it runs](where-it-runs.md).

A worker container receives the workspace, `PEEN_CONFIG_DIR` read-only, the global learned directory writable while `PEEN_AUTO_EVOLVE` is on, its own session directory, and only the runtime configuration it needs, including the named provider credential for its model calls. It does not receive `PEEN_STATE_DIR`, the API token, or the controller's Docker socket. A profile with `docker: full` gets that socket, and one with `docker: readonly` gets a Docker API that answers only read requests. The agent's commands run as the same user as the worker process that holds the provider credential. The worker is non-dumpable, so they cannot read the credential from the worker's `/proc` files, but an agent with root in the container, as `sudo: true` gives, may reach it. Peen runs `PEEN_WORKER_IMAGE` when it is set, then the Docker profile's `image`, then the image published alongside the running build. The container starts as root and the Peen entrypoint drops it back to your host account, so an image without that entrypoint fails when the worker starts.

## Transcripts

Tool calls and their results are recorded verbatim in the session transcript and sent live over the global WebSocket feed, like any other message. If the agent reads a file containing a secret, or a command prints one to stdout, that secret is now in the SQLite transcript and in every connected client that did not ask for one session only. Peen does not scan for or redact secret-shaped content in tool output. Treat the transcript and the event stream as sensitive as the files and commands the agent can reach. The starter [security rule](../examples/rules/security.md) asks the agent not to print secrets, `.env` contents, or environment dumps, and to show a variable's name instead of its value. It is a request to the model, not redaction, so it lowers the chance of a leak without preventing one.

## Prompt injection

A file, a web page a command fetched, a command's output, or another agent's reply can carry text written to steer the agent. Peen wraps every tool result in a `<toolOutput>` block that names the tool and where its text came from, and ends the result with a notice. The notice says the text is data from that source, did not come from the user, and holds no instructions to follow, and that only the user's messages can ask for new actions. The default system prompt says the same. Peen escapes `<toolOutput` and `</toolOutput` inside a result, so planted text cannot close the block early. The wrapped result, notice included, fits within `PEEN_MAX_TOOL_RESULT_TOKENS`. Peen shortens the raw text to make room. Results of `ask` and `useSkill` are not wrapped, because an answer to `ask` comes from the user or the parent agent and a skill holds instructions the agent loads on purpose. A hook's `deny` reason is not wrapped either, because it is your own configuration. Hooks see the raw result before Peen wraps it. The transcript and the WebSocket feed carry the wrapped result, as the model read it.

The wrapper helps the model recognize injected instructions. It does not make them harmless, and a model can still follow them. The [agent space](#docker-agent-spaces) stays the boundary that limits what a misled agent can reach.

[Auto-evolve](harness.md#auto-evolve) makes a successful injection last. An agent misled into saving a planted instruction as a learned rule or skill hands it to every later session that reads that learned directory, and the global learned directory reaches every session. The prompt tells the agent to save only what the user's messages and its own verified work taught it, which lowers the chance without preventing it. Review `.agents/learned/` like any other harness change. `PEEN_WRITABLE_CONFIG_DIR` widens this to your whole global harness, hooks included, and a hook runs commands in every session.

## Unattended sessions

A session with no client attached keeps running. Nothing but Stop, cancelling a child, shutting down the controller or an embedded `Close`, a failed turn, or a crash stops it, so an unattended session keeps calling the model and spending provider budget until something explicitly stops it.

## Network exposure

`PEEN_API_TOKEN` is empty by default, and an empty token turns authentication off. Set it and use `wss://` outside local development. Browser WebSockets authenticate with subprotocols, because browsers cannot attach an `Authorization` header. The [API reference](http-api.md) has the handshake.

## Running it

Run Peen as a non-root user, in a container, with only the mounts, network access, and capabilities the deployment needs. The installer's default does this. [Deployment](deployment.md#docker) covers production mounts, networking, and container hardening.
