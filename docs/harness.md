# The harness

The harness is everything Peen tells the model besides your message: `AGENTS.md` instructions, rules, skills, named agents, event handlers, and hooks. You write it as plain files beside your code. Peen reads those files fresh at the start of every turn, so an edit takes effect on the next message without a restart. The two exceptions are `SYSTEM.md` and `APPEND_SYSTEM.md`, which Peen reads once when it starts.

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

1. The embedded base: Peen's own operating rules, the core skills `planning`, `freshness`, `delegation`, and `plan-and-delegate`, and the core root agent `general-purpose`.
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
- Custom skills, custom named agents, and event handlers replace by name. A later layer's `reviewer` agent replaces an earlier `reviewer` as a whole. Inside one layer, `.agents/skills/x` wins over `.claude/skills/x`.
- The embedded skills and agent are core and are never replaced. A custom skill or agent with the same name sits beside the core one. See [Core and custom skills and agents](#core-and-custom-skills-and-agents).
- Hook groups always add, in layer order, and every layer's hooks run.

## Core and custom skills and agents

Peen keeps two groups. Core skills and agents are embedded in the binary. They are the skills `planning`, `freshness`, `delegation`, and `plan-and-delegate`, and the agent `general-purpose`. Custom skills and agents come from the layers above. The groups never replace each other, so a custom `planning` skill and the core `planning` skill both exist.

Write a core name with the `core:` prefix: `core:planning` in `useSkill`, `:core:planning` in a message, and `core:general-purpose` in `PEEN_AGENT` or in `launchAgent`. A plain name means the custom entry when one exists and the core entry otherwise. `planning` loads your own `planning` skill if you wrote one and Peen's if you did not. A custom name cannot contain `:`, so a custom entry can never take a `core:` name.

The skill and agent catalogues list the core group first, with the `core:` names, then the custom group.

To turn core entries off, set `PEEN_DISABLED_CORE_SKILLS` and `PEEN_DISABLED_CORE_AGENTS` to comma-separated core names without the prefix, or to `all`. A disabled entry is gone from the catalogue, from `useSkill` and `:name`, and from agent lookup. A custom entry of the same name stays. Startup fails on an unknown name, and when `PEEN_AGENT` is a `core:` name that is disabled, which includes the default `core:general-purpose`. A plain `PEEN_AGENT` name passes startup and fails at a turn whose workspace defines no such custom agent. To disable `general-purpose`, set `PEEN_AGENT` to a custom agent. Each worker gets both settings, so a worker resolves the same harness as the controller. The harness hash changes when either setting changes.

Peen's embedded operating rules are always on and have no switch. Later `AGENTS.md` files add to them and cannot replace them. They tell the agent to write a plan at `.plans/<UTC datetime>-<scope>.md`, for example `.plans/20261010T120501Z-fix-login.md`, before any request that changes code, config, docs, or tests. A pure question gets no plan. The agent adds `.plans/` to the workspace `.gitignore` when it is missing, and a child agent writes its own plan and links its parent's. The `core:planning` skill describes the plan.

## When it is read

Peen resolves the whole harness at the start of every turn, and again when it checks whether a session event should wake an idle session. Apart from the base prompt files described below, nothing is kept between turns, and there is no file watcher. During one turn the harness does not change. A file you edit mid-turn is picked up by the next turn, and child agents use the same snapshot as the turn that launched them.

Each turn stores the exact harness it used as a context snapshot, with a manifest of every source file and its hash. Read it with `GET /v1/session/context-snapshots/{contextHash}` from the [API](http-api.md).

## What the model sees

The system prompt for a root turn is assembled in this order:

1. The base prompt: Peen's embedded prompt, or `PEEN_CONFIG_DIR/SYSTEM.md` when present, followed by `APPEND_SYSTEM.md` when present. Peen reads these two files once at startup. A message can append to or replace this base for one turn with `systemPrompt`.
2. Every `AGENTS.md` and rule block, in layer order, then each `AGENTS.md` below the workspace with a line naming the directory it covers.
3. The root agent's instructions, from the agent named by `PEEN_AGENT` (default `core:general-purpose`).
4. The skill catalogue: one line per skill with its name, description, and source path, core skills first under their `core:` names, then custom skills.
5. The named-agent catalogue: one line per agent with its name, description, allowed tools, and source path, core agents first under their `core:` names, then custom agents.
6. Harness diagnostics, when a file was invalid.
7. The workspace path.
8. Runtime facts: local time, timezone, operating system, architecture, CPU count, and Go version.
9. Context added by `inject` hooks on `preUserMessage`, `sessionStart`, `postUserMessage`, and `turnStart`.

A skill you name with `:skill-name` is not part of the system prompt. It reaches the model as its own `<skill>` user message right after your message, see [Forcing a skill](skills.md#forcing-a-skill).

## Mistakes and limits

An invalid optional file never breaks the turn. Peen skips that one file, keeps everything else, and reports it three ways:

- a log line, `invalid optional harness configuration ignored`;
- a durable `harness.warning` event before `turn.started`, with `{"warnings":[{"kind","source","reason"}]}`, which the control surface shows as a workspace configuration warning. `kind` is `instruction` (an `AGENTS.md`), `rule`, `skill`, `agent`, `event-handler`, or `hook`, and `source` is the file's path;
- a diagnostics block in the system prompt, so the model knows the file was ignored.

Typical reasons are an empty rule file, a skill whose `name` does not match its directory, unknown frontmatter keys, and an unknown hook event. A skill whose body names a skill that does not exist with `:name` is reported too, and the reference is skipped when the skill is delivered. A `:skill-name` in your message that names an ignored skill still fails the message.

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
