# Rules

A rule is plain Markdown that Peen puts into the system prompt on every turn. Use rules for the things the agent must know every time: how to build and test, where things live, what it must not touch, and your conventions. If something only matters for one kind of task, make it a [skill](skills.md). If it must be enforced rather than requested, use a [hook](hooks.md).

## Where rule files go

Peen reads three kinds of rule file in every [harness layer](harness.md#where-peen-looks):

| File | Use it for |
| --- | --- |
| `AGENTS.md` | The main rules for that directory |
| `.agents/rules/<name>.md` | One topic per file, such as `testing.md` or `go.md` |
| `.claude/rules/<name>.md` | The same, for projects that already keep Claude Code rules here |

Only direct `.md` children of a `rules/` directory count. Subdirectories are not searched. `CLAUDE.md` is not read.

A typical layout:

```text
/home/me/work/
  AGENTS.md                  # applies to every project under work/
  my-api/
    AGENTS.md                # applies to the my-api workspace
    .agents/
      rules/
        go.md
        database.md
```

When you open `/home/me/work/my-api`, the model gets `work/AGENTS.md`, then `my-api/AGENTS.md`, then `go.md`, then `database.md`.

## Format

A rule file is ordinary Markdown with no frontmatter. Write it the way you would brief a new teammate.

```markdown
# Project rules

- Build with `make build`. Run tests with `make test` before you finish.
- Every shell script starts with `#!/bin/bash`, a comment saying what it does, and `set -eu`.
- Never edit files under `vendor/`.
```

An empty or whitespace-only file is invalid and is skipped with a warning.

## How rules combine

Rules only add. Every rule file from every layer becomes its own block in the system prompt, in layer order: Peen's embedded operating rules, then `PEEN_CONFIG_DIR`, then each directory from `/` down to the workspace. Inside one layer the order is `AGENTS.md`, sorted `.claude/rules/*.md`, then sorted `.agents/rules/*.md`. A later file never replaces an earlier one, so when two rules conflict, put the more specific instruction in the deeper layer and say that it overrides the general one.

A user message cannot rewrite rule blocks. A message's `systemPrompt` only changes Peen's base prompt for that turn.

## When rules load

Peen reads every rule file at the start of every turn. Edit `AGENTS.md`, send the next message, and the model sees the new text. A turn that is already running keeps the rules it started with.

Rules load per workspace, not per file. Peen does not read `AGENTS.md` files in subdirectories below the workspace, and nothing is loaded because the model opens a file in some directory. To give the model a rule the first time it changes a particular kind of file, use the [hook recipe](hooks.md#example-hand-the-model-a-rule-once).

## Check what the model got

Every turn stores a context snapshot listing each source file and its hash, and a prompt snapshot with the exact system prompt. Read them through `GET /v1/session/context-snapshots/{contextHash}` and `GET /v1/session/prompt-snapshots/{promptHash}` in the [API](http-api.md). A rule file that was skipped shows up as a workspace configuration warning in the chat and as a `harness.warning` event.

## Limits

At most 64 rule and instruction files across all layers, 128 KiB per file, and 1 MiB for the whole harness. Going over a limit fails the turn. See [the harness limits](harness.md#mistakes-and-limits).
