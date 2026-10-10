# The harness

The harness is everything Peen tells the model besides your message: `AGENTS.md` instructions, rules, skills, named agents, event handlers, and hooks. You write it as plain files beside your code. Peen reads those files fresh at the start of every turn, so an edit takes effect on the next message without a restart.

| Piece | File | What it does | Details |
| --- | --- | --- | --- |
| Instructions | `AGENTS.md` | The project's instructions file in the agents.md format, always in the system prompt | [AGENTS.md and rules](rules.md) |
| Rules | `.agents/rules/*.md`, `.claude/rules/*.md` | One topic per file, always in the system prompt | [AGENTS.md and rules](rules.md) |
| Skills | `.agents/skills/<name>/SKILL.md`, `.claude/skills/<name>/SKILL.md` | Named procedures the model loads when a task fits | [Skills](skills.md) |
| Named agents | `.agents/agents/<name>.md` | Child agents with their own instructions and tool set | [Agents](agents.md) |
| Event handlers | `.agents/events/<type>.md` | What to do when a session event arrives, and whether it starts a turn | [Session events](events.md) |
| Hooks | `.agents/hooks.yaml` | Commands, denials, injected context, and events around lifecycle and tool calls | [Hooks](hooks.md) |

## Where Peen looks

Peen builds the harness from layers. It reads them in this order, and each later layer is more specific:

1. The embedded base: Peen's own operating rules, the `planning` and `freshness` skills, and the `default` root agent.
2. `PEEN_CONFIG_DIR`, the trusted operator layer.
3. Every directory from the filesystem root `/` down to the session's workspace, in that order.

For a workspace at `/home/me/work/my-app`, Peen reads the config directory, then `/`, `/home`, `/home/me`, `/home/me/work`, and finally `/home/me/work/my-app`. Put instructions that apply to every project in `/home/me/work/AGENTS.md` and project instructions in `/home/me/work/my-app/AGENTS.md`. Missing files and directories are normal and cost nothing. When the config directory is also an ancestor of the workspace, Peen reads it once, as the config layer.

Layers stop at the workspace. Below it, Peen reads only `AGENTS.md` files, each scoped to its own directory, after every layer. Rules, skills, agents, event handlers, and hooks below the workspace are not read, and nothing loads because the model touched a file. To hand the model a rule the first time it changes a matching file, use the [hook recipe](hooks.md#example-hand-the-model-a-rule-once). [AGENTS.md and rules](rules.md#agentsmd) covers nested `AGENTS.md` files.

Inside one layer Peen reads, in this order:

1. `AGENTS.md`
2. `.claude/rules/*.md`
3. `.agents/rules/*.md`
4. `.claude/skills/*/SKILL.md`
5. `.agents/skills/*/SKILL.md`
6. `.agents/agents/*.md`
7. `.agents/events/*.md`
8. `.agents/hooks.yaml`

Directory entries are sorted bytewise, so the order is stable. The `.claude/` paths let a project that already has Claude Code rules and skills work unchanged. Peen does not read `CLAUDE.md`.

## How layers combine

- `AGENTS.md` files and rules always add. Each file from every layer becomes its own block in the system prompt, broad layers first, then the `AGENTS.md` files below the workspace.
- Skills, named agents, and event handlers replace by name. A later layer's `reviewer` agent replaces an earlier `reviewer` as a whole. Inside one layer, `.agents/skills/x` wins over `.claude/skills/x`. You can replace the embedded `planning` and `freshness` skills and the `default` agent the same way.
- Hook groups always add, in layer order, and every layer's hooks run.

## When it is read

Peen resolves the whole harness at the start of every turn, and again when it checks whether a session event should wake an idle session. Nothing is cached between turns and there is no file watcher. During one turn the harness is frozen: a file you edit mid-turn is picked up by the next turn, and child agents use the same snapshot as the turn that launched them.

Each turn stores the exact harness it used as a context snapshot, with a manifest of every source file and its hash. Read it with `GET /v1/session/context-snapshots/{contextHash}` from the [API](http-api.md).

## What the model sees

The system prompt for a root turn is assembled in this order:

1. The base prompt: Peen's embedded prompt, or `PEEN_CONFIG_DIR/SYSTEM.md` when present, followed by `APPEND_SYSTEM.md` when present. Peen reads these two files once at startup. A message can append to or replace this base for one turn with `systemPrompt`.
2. Every `AGENTS.md` and rule block, in layer order, then each `AGENTS.md` below the workspace with a line naming the directory it covers.
3. The root agent's instructions, from the agent named by `PEEN_AGENT` (default `default`).
4. The skill catalogue: one line per skill with its name, description, and source path.
5. The named-agent catalogue: one line per agent with its name, description, allowed tools, and source path.
6. Harness diagnostics, when a file was invalid.
7. The full text of any skill the message activated with `:skill-name`.
8. The workspace path.
9. Runtime facts: local time, timezone, operating system, architecture, CPU count, and Go version.
10. Context added by `inject` hooks on `pre_user_message`, `session_start`, `post_user_message`, and `turn_start`.

## Mistakes and limits

An invalid optional file never breaks the turn. Peen skips that one file, keeps everything else, and reports it three ways:

- a log line, `invalid optional harness configuration ignored`;
- a durable `harness.warning` event before `turn.started`, with `{"warnings":[{"kind","source","reason"}]}`, which the control surface shows as a workspace configuration warning. `kind` is `instruction` (an `AGENTS.md`), `rule`, `skill`, `agent`, `event-handler`, or `hook`, and `source` is the file's path;
- a diagnostics block in the system prompt, so the model knows the file was ignored.

Typical reasons are an empty rule file, a skill whose `name` does not match its directory, unknown frontmatter keys, and an unknown hook event. A `:skill-name` that names an ignored skill still fails the message.

Size limits are hard errors and fail the turn, because Peen cannot safely continue past them. They are fixed:

| Limit | Value |
| --- | --- |
| Harness files in total | 256 |
| `AGENTS.md` and rule files | 64 |
| Skills | 64 |
| Named agents | 64 |
| Event handlers | 64 |
| Hook groups | 256 |
| Entries in one directory | 1024 |
| One file | 128 KiB |
| All files together | 1 MiB |

An unreadable layer directory also fails the turn. Symlinks are resolved and only regular files are read.

`AGENTS.md` files below the workspace are the exception. Peen searches at most 10,000 directories there, and a nested file that goes over a limit, or a directory it cannot read, is skipped with a warning while the turn runs with the files that fit.
