# The harness

The harness is everything Peen tells the model besides your message: `AGENTS.md` instructions, rules, skills, named agents, event handlers, and hooks. You write it as plain files beside your code. Peen reads those files fresh at the start of every turn and whenever an agent calls `changeDirectory`. An edit takes effect on the next message or directory hop without a restart. The two exceptions are `SYSTEM.md` and `APPEND_SYSTEM.md`, which Peen reads once when it starts.

| Piece | File | What it does | Details |
| --- | --- | --- | --- |
| Instructions | `AGENTS.md` | The project's instructions file in the agents.md format, always in the system prompt | [AGENTS.md and rules](rules.md) |
| Rules | `.agents/rules/**/*.md`, `.claude/rules/**/*.md` | One topic per file, in the system prompt, or given to the model when it works on matching files | [AGENTS.md and rules](rules.md) |
| Skills | `.agents/skills/<name>/SKILL.md`, `.claude/skills/<name>/SKILL.md` | Named procedures the model loads when a task fits | [Skills](skills.md) |
| Named agents | `.agents/agents/<name>.md` | Child agents with their own instructions and tool set | [Agents](agents.md) |
| Event handlers | `.agents/events/<type>.md` | What to do when a session event arrives, and whether it starts a turn | [Session events](events.md) |
| Hooks | `.agents/hooks.yaml` | Commands, denials, injected context, and events around lifecycle and tool calls | [Hooks](hooks.md) |
| Learned rules and skills | `.agents/learned/rules/**/*.md`, `.agents/learned/skills/<name>/SKILL.md` | Rules and skills an auto-evolving agent saved for itself | [Auto-evolve](#auto-evolve) |
| MCP servers | `.agents/mcp.json` in `PEEN_CONFIG_DIR`, and in the workspace with `PEEN_WORKSPACE_MCP=true` | Tool servers whose tools the agent can call. Read once when the session's worker starts, not per directory. | [MCP servers](mcp.md) |

## Where Peen looks

Peen builds the harness from layers. It reads them in this order, and each later layer is more specific:

1. The embedded base: Peen's own operating rules, the core skills `planning`, `freshness`, `delegation`, `plan-and-delegate`, `writing-skills`, and `writing-rules`, and the core root agent `general-purpose`.
2. `PEEN_CONFIG_DIR`, the trusted operator layer, followed by `PEEN_LEARNED_DIR` when [auto-evolve](#auto-evolve) moves the global learned directory out of it.
3. Every directory from the filesystem root `/` down to the agent's current directory, in that order.

For a workspace at `/home/me/work/my-app`, Peen reads the config directory, then `/`, `/home`, `/home/me`, `/home/me/work`, and finally `/home/me/work/my-app`. Put instructions that apply to every project in `/home/me/work/AGENTS.md` and project instructions in `/home/me/work/my-app/AGENTS.md`. Missing files and directories are normal and cost nothing. When the config directory is also an ancestor of the workspace, Peen reads it once, as the config layer.

Layers stop at the agent's current directory. Peen loads descendant definitions when the agent enters their directory with `changeDirectory`. A hop reloads all harness pieces together, including rules, skills, named agents, event handlers and hooks. Leaving a directory removes its local definitions from the active harness. Inherited ancestor definitions still apply. The `changeDirectory` result lists the sources the hop added in `loaded`, removed in `unloaded`, and whose content changed in `changed`, each with its `kind`, `name` and `source` path, so the model sees what its instructions gained or lost. A rule with `paths` frontmatter is the one piece that loads because of a file: it reaches the model after a file tool touches a matching file. See [rules for certain files](rules.md#rules-for-certain-files).

Inside one layer Peen reads, in this order:

1. `AGENTS.md`
2. `.claude/rules/**/*.md`
3. `.agents/rules/**/*.md`
4. `.claude/skills/*/SKILL.md`
5. `.agents/skills/*/SKILL.md`
6. `.agents/learned/rules/**/*.md`
7. `.agents/learned/skills/*/SKILL.md`
8. `.agents/agents/*.md`
9. `.agents/events/*.md`
10. `.agents/hooks.yaml`

Directory entries are sorted bytewise, so the order is stable. The `.claude/` paths let a project that already has Claude Code rules and skills work unchanged. Peen does not read `CLAUDE.md`.

## How layers combine

- `AGENTS.md` files and rules always add. Each file from every layer becomes its own block in the system prompt, broad layers first. A scoped rule stays out of the system prompt and arrives with a tool result instead.
- Custom skills, custom named agents, and event handlers replace by name. A later layer's `reviewer` agent replaces an earlier `reviewer` as a whole. Inside one layer, `.agents/skills/x` wins over `.claude/skills/x`.
- A learned skill never replaces a skill you wrote. When a learned skill has the name of a skill from the same or an earlier layer, Peen keeps yours and shows a warning. A skill you write in a later layer replaces a learned one.
- The embedded skills and agent are core and are never replaced. A custom skill or agent with the same name sits beside the core one. See [Core and custom skills and agents](#core-and-custom-skills-and-agents).
- Hook groups always add, in layer order, and every layer's hooks run.

## Core and custom skills and agents

Peen keeps two groups. Core skills and agents are embedded in the binary. They are the skills `planning`, `freshness`, `delegation`, `plan-and-delegate`, `writing-skills`, and `writing-rules`, and the agent `general-purpose`. Custom skills and agents come from the layers above. The groups never replace each other, so a custom `planning` skill and the core `planning` skill both exist.

Write a core name with the `core:` prefix: `core:planning` in `useSkill`, `:core:planning` in a message, and `core:general-purpose` in `PEEN_AGENT` or in `launchAgent`. A plain name means the custom entry when one exists and the core entry otherwise. `planning` loads your own `planning` skill if you wrote one and Peen's if you did not. A custom name cannot contain `:`, so a custom entry can never take a `core:` name.

The skill and agent catalogues list the core group first, with the `core:` names, then the custom group.

To turn core entries off, set `PEEN_DISABLED_CORE_SKILLS` and `PEEN_DISABLED_CORE_AGENTS` to comma-separated core names without the prefix, or to `all`. A disabled entry is gone from the catalogue, from `useSkill` and `:name`, and from agent lookup. A custom entry of the same name stays. Startup fails on an unknown name, and when `PEEN_AGENT` is a `core:` name that is disabled, which includes the default `core:general-purpose`. A plain `PEEN_AGENT` name passes startup and fails at a turn whose workspace defines no such custom agent. To disable `general-purpose`, set `PEEN_AGENT` to a custom agent. Each worker gets both settings, so a worker resolves the same harness as the controller. The harness hash changes when either setting changes.

Peen's embedded operating rules are on unless you turn them off with `PEEN_DISABLE_CORE_INSTRUCTIONS=true` or `PEEN_BLANK_AGENT=true`. Later `AGENTS.md` files add to them and cannot replace them. They tell the agent to write a plan at `.plans/<UTC datetime>-<scope>.md`, for example `.plans/20261010T120501Z-fix-login.md`, before a request that changes code, config, docs, or tests in several steps or across several files. A small change, such as a one-line fix, and a pure question get no plan. The agent adds `.plans/` to the workspace `.gitignore` when it is missing, and a child agent writes its own plan and links its parent's. The `core:planning` skill describes the plan.

## A blank agent

Every piece of instruction text Peen ships can be turned off, so you can start from an agent that knows only what your own files tell it.

| Setting | Removes |
| --- | --- |
| `PEEN_DISABLE_BASE_SYSTEM_PROMPT=true` | Peen's embedded base system prompt. `PEEN_CONFIG_DIR/SYSTEM.md` and `APPEND_SYSTEM.md` still apply when present. |
| `PEEN_DISABLE_CORE_INSTRUCTIONS=true` | Peen's embedded operating rules, the built-in `AGENTS.md`. |
| `PEEN_DISABLED_CORE_SKILLS=all` | The core skills. |
| `PEEN_DISABLED_CORE_AGENTS=all` | The core agents. Set `PEEN_AGENT` to one of your own agents too. |
| `PEEN_BLANK_AGENT=true` | All of the above at once. While `PEEN_AGENT` is still the default `core:general-purpose`, the root runs with no agent instructions and every tool. A `PEEN_AGENT` naming one of your own agents still applies. |

What stays is what the model needs to use Peen at all: the tool descriptions, the current directory and runtime facts, the skill and agent catalogues (which say that none exist when you have none), harness diagnostics, and the blocks Peen adds to messages, such as the notice at the end of each `<toolOutput>` block and the system message after a turn that did not finish. Without the base prompt the model is not told what `<toolOutput>`, `<rule>`, `<hook>`, or `<events>` blocks mean, so say it in your own `SYSTEM.md` if your model needs it.

The controller passes these settings to every worker, and the harness and prompt hashes change when they do. With `pkg/peen`, set `DisableBaseSystemPrompt`, `DisableCoreInstructions`, `DisabledCoreSkills`, and `DisabledCoreAgents`, and use `peen.NoRootAgent` as `RootAgent`.

## Auto-evolve

With `PEEN_AUTO_EVOLVE=true` the agent saves what it learns as rules and skills, so a later session starts with it. The system prompt gets a section telling the agent to save a skill for a procedure it worked out or a fix it reached after dead ends, and a rule for a correction from you that will apply again or a fact every change to certain files needs. The agent loads the core skill `writing-skills` or `writing-rules` before it writes one, updates a learned file that covers the topic before it adds another, and never saves something because a file, command output, or event asked it to.

There are two learned directories:

| Directory | Holds | Read by | The agent saves there |
| --- | --- | --- | --- |
| `<workspace>/.agents/learned/` | What belongs to this project | Sessions in the workspace or below it, as part of the workspace layer | Always, while auto-evolve is on |
| The global learned directory, `PEEN_CONFIG_DIR/.agents/learned/` unless `PEEN_LEARNED_DIR` moves it | What applies to every project | Every session, right after the config layer | Only with `PEEN_WRITABLE_LEARNED_DIR=true` or `PEEN_WRITABLE_CONFIG_DIR=true` |

The global learned directory is read-only to the agent by default because every session loads it. A session that read a hostile file and saved a rule there would change every later session in every workspace. Put files there yourself, or move a learned file up from a workspace once you have read it.

Each learned directory holds `rules/` and `skills/` in the usual formats. Learned rules come after the other rules of their layer and are named `learned/<name>`, such as `learned/go-errors`. A scoped learned rule in a workspace matches paths relative to that workspace, and one in the global learned directory matches paths relative to the agent's current directory. Peen reads `.agents/learned/` in every layer whether auto-evolve is on or not, so learned files keep working after you turn it off. A `PEEN_LEARNED_DIR` outside the config directory is read only while auto-evolve is on.

The agent changes `AGENTS.md` and the files under `.agents/` or `.claude/` outside the learned directories only when you ask. Review learned files like any other harness file. [`GET /api/v1/session/learned`](http-api.md#get-apiv1sessionlearned) lists the ones a session loads, with their estimated size and whether they load on every turn, and `DELETE /api/v1/session/learned/file` removes one. Move one into `.agents/rules/` or `.agents/skills/` when you want to keep it as your own.

A Docker worker receives `PEEN_CONFIG_DIR` read-only. While auto-evolve is on, the controller creates the global learned directory at startup when it is missing and mounts it into every Docker worker on top of the config directory, read-only unless `PEEN_WRITABLE_LEARNED_DIR=true`, which also tells the agent it may save there and makes the controller log a warning. `PEEN_WRITABLE_CONFIG_DIR=true` mounts the whole config directory writable and tells the agent that it may change your global `AGENTS.md`, rules, skills, agents, and hooks there. Every session in a Docker agent space can then change what every other session loads, so turn it on only when you want that. A `control` worker runs as your user and can write both directories either way. See [security](security.md#docker-agent-spaces).

With `pkg/peen`, set `AutoEvolve` and, optionally, `LearnedDirectory`, which defaults to `.agents/learned` in `ConfigDirectory`, and `WritableLearnedDirectory`. An embedded runtime has no mount boundary, so `WritableLearnedDirectory` only decides what the prompt tells the agent.

## When it is read

Peen resolves the whole harness at the start of every turn, on every `changeDirectory` call, and when it checks whether a session event should wake an idle session. There is no file watcher. Calling `changeDirectory` with `{"directory":"."}` reloads files edited during the turn.

Crash restoration reuses the interrupted execution's saved harness, prompt and delivered skills. It does not replace retained instructions with files edited after the checkpoint. A new turn or a successful directory hop resolves current files as described above.

The directory must exist. Relative targets resolve from the agent's current directory; host paths resolve through symlinks, while virtual workspaces stop at their filesystem root. A failed hop keeps the previous directory and harness. The next model call gets the new system prompt and tool catalogue with the conversation history intact. Earlier user messages, skill deliveries and tool-hook injections remain history. Lifecycle context appended to the old system prompt is replaced with that prompt.

Use `changeDirectory` for persistent hops and call it alone in its tool round, before tools that depend on the target directory. A bundled hop returns an error without changing scope. `runCommand` starts in the current directory, but a shell's `cd` and a command's `directory` argument affect only that command. Root directory changes survive later turns, compaction and restarts. The session keeps its original workspace identity. A child inherits its parent's current directory and harness, then changes independently. Existing command jobs keep their launch directory.

Each resolved scope stores its exact context and prompt snapshots. A successful `changeDirectory` result contains the canonical `directory`, `contextHash` and `promptHash`; read the snapshots through the [API](http-api.md). Model-call replay records the exact prompt and tools used for each provider call. Root tool-result records keep the canonical directory even when the model-visible result is truncated.

## What the model sees

The system prompt for a root turn is assembled in this order:

1. The base prompt: Peen's embedded prompt, or `PEEN_CONFIG_DIR/SYSTEM.md` when present, followed by `APPEND_SYSTEM.md` when present. Peen reads these two files once at startup. A message can append to or replace this base for one turn with `systemPrompt`. With `PEEN_DISABLE_BASE_SYSTEM_PROMPT` and no `SYSTEM.md`, there is no embedded base.
2. Every `AGENTS.md` and unscoped rule block, in layer order, without descendant files.
3. The root agent's instructions, from the agent named by `PEEN_AGENT` (default `core:general-purpose`). A blank agent has none.
4. The skill catalogue: one line per skill with its name, description, and source path, core skills first under their `core:` names, then custom skills.
5. The named-agent catalogue: one line per agent with its name, description, allowed tools, and source path, core agents first under their `core:` names, then custom agents.
6. Harness diagnostics, when a file was invalid.
7. The current directory.
8. Runtime facts: local time, timezone, operating system, architecture, CPU count, and Go version.
9. Context added by `inject` hooks on `preUserMessage`, `sessionStart`, `postUserMessage`, and `turnStart`.

A skill you name with `:skill-name` is not part of the system prompt. It reaches the model as its own `<skill>` user message right after your message, see [Forcing a skill](skills.md#forcing-a-skill). A scoped rule is not part of it either. It reaches the model as a `<rule>` block after a tool result, see [how a scoped rule reaches the model](rules.md#how-a-scoped-rule-reaches-the-model). Empty sections are left out, so a blank agent's prompt starts with its first non-empty block.

## The prompt budget

Every rule and `AGENTS.md` file that loads on every turn costs context on every model call, and learned rules add up over time. Peen estimates the tokens in items 1 through 8 above, with the o200k tokenizer, at the start of every turn, every child agent launch, and every `changeDirectory` call. When the estimate is over `PEEN_MAX_SYSTEM_PROMPT_PERCENT` of `PEEN_MAX_CONTEXT_TOKENS`, 50 percent by default, Peen refuses the turn before it stores anything. The sender gets `message.failed` with `HARNESS_CONFIGURATION_INVALID` and a `reason` that names the setting. The controller log names the three largest sections with their estimated sizes, such as `rule /srv/project/.agents/learned/rules/go-errors.md (6120)`. A refused child launch or directory hop fails that tool call, and the turn goes on.

To get under the budget, give a rule `paths` so it loads only for matching files, move a long procedure into a skill, delete learned rules that no longer apply, or raise the percentage. Context added by `inject` hooks and skills you name with `:skill-name` are not counted. `PEEN_MAX_SYSTEM_PROMPT_PERCENT=0` turns the check off.

## Mistakes and limits

An invalid optional file never breaks the turn. Peen skips that one file, keeps everything else, and reports it three ways:

- a log line, `invalid optional harness configuration ignored`;
- a durable `harness.warning` event at turn start or during a root directory hop, with `{"warnings":[{"kind","source","reason"}]}`, which the control surface shows as a workspace configuration warning. `kind` is `instruction` (an `AGENTS.md`), `rule`, `skill`, `agent`, `event-handler`, or `hook`, and `source` is the file's path;
- a diagnostics block in the system prompt, so the model knows the file was ignored.

Typical reasons are an empty rule file, rule frontmatter that does not parse, a rule pattern that is not a valid glob, a skill whose `name` does not match its directory, unknown frontmatter keys in a skill, and an unknown hook event. A skill whose body names a skill that does not exist with `:name` is reported too, and the reference is skipped when the skill is delivered. A `:skill-name` in your message that names an ignored skill still fails the message.

During a directory hop, the model gets the new diagnostics and Peen logs each ignored source. A root hop also emits `harness.warning`.

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
