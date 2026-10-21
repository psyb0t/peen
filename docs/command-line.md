# Command line

The `peen` command the installer puts on your `PATH` runs one turn from a shell, opens and watches sessions, and checks the controller. The server binary you build yourself takes the same turn and client commands. [Deployment](deployment.md#the-peen-command) lists the commands that install, start, stop, and upgrade Peen.

## Run one turn

Run one turn in the current directory with the installed `peen` command:

```bash
peen --prompt "fix the tests"
peen --model provider/model --output text --prompt "explain this project"
printf '%s\n' 'review the current changes' | peen --prompt -
```

Peen opens or resumes the directory's durable session, sends the prompt over WebSocket, streams the session's frames, and exits when the turn ends. It reuses a controller answering at `PEEN_HTTP_LISTEN_ADDRESS` and leaves it running. With none running, it starts a controller inside the command, serves the API through an in-memory listener with no TCP or metrics port, and stops it after the turn. A prompt sent during an active turn is queued into that turn, and the command follows it to its end. Other connected clients see the same work.

## Output and exit status

Output defaults to JSONL, preserving every WebSocket frame on stdout. `--output text` puts reply text on stdout and thinking, tools, child agents, and failures on stderr. Logs go to stderr at `warn` unless `LOG_LEVEL` is set.

Exit status is 0 for completion, 1 for failure, and 130 for cancellation. The first interrupt requests cancellation and waits for confirmation, the second stops streaming and shuts down, and the third exits immediately. A question without a terminal available to answer cancels the turn.

## Flags

A word that is not a `peen` command is refused, so a mistyped command never sends a prompt. The server binary takes `peen exec [flags] (--prompt <prompt> | <prompt>)`, and the installed command passes `peen exec` through too. The prompt is the `--prompt` value or the arguments joined with spaces, not both, and a prompt of `-` is read from stdin.

| Flag | Meaning |
| --- | --- |
| `--workspace DIR` | Run the turn in another directory's session. |
| `--model provider/model` | The model for this turn. |
| `--reasoning-effort LEVEL` | The reasoning level for this turn. |
| `--system-prompt TEXT` | Instructions for this turn. |
| `--system-prompt-mode append\|replace` | Add the instructions to the system prompt or replace it. |
| `--agent-space control\|docker\|docker:<profile>` | The session's agent space. An existing session moves when needed. `docker` means `docker:default`. |
| `--output text` | Readable output instead of JSONL. |
| `--control-space host\|docker` | Installed command only. Where the controller client runs for this invocation. |

Defaults come from your configuration. The installed command lets exported `PEEN_*` and logging settings override its `.env` for the run, except `PEEN_HOME`, `PEEN_INSTALL_*`, and `PEEN_NO_BROWSER`.

## Session and control commands

The Peen server binary is also a local control client. With the installer, the `peen` command passes `session` and `control` to that binary with your installation's configuration. These commands talk to a running controller over its HTTP API:

```bash
peen control status
peen session open ~/work/my-app
peen session list
peen session attach <session-id>
peen session stop <session-id>
```

`peen control status` reports whether the controller answers and starts nothing, so it tells "not running" apart from "running". `peen session open` prints the session ID, its workspace, and whether the call created the session or resumed one. `peen session attach` streams that session's live events, starts no turn, and reconnects automatically after an unexpected drop, catching up from the session feed; `--cursor <n>` replays from sequence `n` first. `peen session stop` pauses unfinished session work and says when there was nothing to stop. Through the `peen` command, the `session` commands start the controller first when it is not running.

## Running the server binary yourself

A server binary you run yourself, from `make install` or a source build, takes the same commands and reads the same `PEEN_` environment as the controller, so a command and its controller agree on the address. Its `session` commands start `peen run` themselves when nothing answers. The commands reach the controller over HTTP only, not over a Unix socket.
