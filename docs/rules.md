# AGENTS.md and rules

Peen puts two kinds of plain Markdown into the system prompt on every turn:

- `AGENTS.md` is a project's instructions file, in the open [agents.md](https://agents.md) format that most coding agents read. It is the README for agents: how to build and test, where things live, what not to touch, and the project's conventions.
- Rules are topic files in `.agents/rules/`, one subject per file, such as `testing.md` or `go.md`. Use them when one `AGENTS.md` would grow too long, or to keep a convention in its own file.

If something only matters for one kind of task, make it a [skill](skills.md). If it must be enforced rather than requested, use a [hook](hooks.md).

## AGENTS.md

An `AGENTS.md` is ordinary Markdown with no frontmatter and no required headings. Write it the way you would brief a new teammate.

```markdown
# my-api

- Build with `make build`. Run tests with `make test` before you finish.
- Every shell script starts with `#!/bin/bash`, a comment saying what it does, and `set -eu`.
- Never edit files under `vendor/`.
```

Peen reads `AGENTS.md` from three places:

| Where | What it covers |
| --- | --- |
| `PEEN_CONFIG_DIR/AGENTS.md` | Every workspace this controller opens |
| Every directory from `/` down to the workspace | The workspace, so `~/work/AGENTS.md` covers every project under `~/work/` |
| Every directory below the workspace | Only the files under that directory |

A typical layout:

```text
/home/me/work/
  AGENTS.md                  # every project under work/
  my-api/
    AGENTS.md                # the my-api workspace
    web/
      AGENTS.md              # files under my-api/web/
```

When you open `/home/me/work/my-api`, the model gets `work/AGENTS.md`, then `my-api/AGENTS.md`, then `web/AGENTS.md`. A file below the workspace starts with a line naming the directory it covers, such as `Instructions from web/AGENTS.md. They apply to files under web/`.

When two `AGENTS.md` files disagree, the one closest to the file being changed wins, as the agents.md format specifies. Peen's own operating instructions tell the model this, and they tell it that your messages in the chat override every `AGENTS.md`.

Below the workspace, Peen skips hidden directories such as `.git`, `node_modules`, `vendor`, and symlinked directories. It searches at most 10,000 directories.

## Rules

A rule is one topic in its own Markdown file. Like `AGENTS.md`, it has no frontmatter.

| File | Use it for |
| --- | --- |
| `.agents/rules/<name>.md` | One topic per file, such as `testing.md` or `go.md` |
| `.claude/rules/<name>.md` | The same, for projects that already keep Claude Code rules here |

Peen reads rule directories from the config directory and from every directory between `/` and the workspace, the same layers as `AGENTS.md`. It does not read rule directories below the workspace. Only direct `.md` children of a `rules/` directory count. Subdirectories are not searched. `CLAUDE.md` is not read.

```text
/home/me/work/my-api/
  AGENTS.md
  .agents/
    rules/
      go.md
      database.md
```

## How they combine

Everything adds. Each file becomes its own block in the system prompt, in this order: Peen's embedded operating instructions, then `PEEN_CONFIG_DIR`, then each directory from `/` down to the workspace, then the `AGENTS.md` files below the workspace, parents before children. Inside one layer the order is `AGENTS.md`, sorted `.claude/rules/*.md`, then sorted `.agents/rules/*.md`. A later file never replaces an earlier one. When a rule and an `AGENTS.md` conflict, put the more specific one in the deeper layer and say that it overrides the general one.

A user message cannot rewrite these blocks. A message's `systemPrompt` only changes Peen's base prompt for that turn.

An empty or whitespace-only file is invalid and is skipped with a warning.

## When they load

Peen reads every `AGENTS.md` and rule file at the start of every turn. Edit one, send the next message, and the model sees the new text. A turn that is already running keeps what it started with.

Nothing is loaded because the model opens a file. To hand the model a rule the first time it changes a particular kind of file, use the [hook recipe](hooks.md#example-hand-the-model-a-rule-once).

## Check what the model got

Every turn stores a context snapshot listing each source file and its hash, and a prompt snapshot with the exact system prompt. Read them through `GET /v1/session/context-snapshots/{contextHash}` and `GET /v1/session/prompt-snapshots/{promptHash}` in the [API](http-api.md). A file that was skipped shows up as a workspace configuration warning in the chat and as a `harness.warning` event.

## Limits

At most 64 `AGENTS.md` and rule files together, 128 KiB per file, and 1 MiB for the whole harness. Going over a limit in the layers from `/` to the workspace fails the turn. Below the workspace, Peen loads the files that fit and skips the rest with a warning. See [the harness limits](harness.md#mistakes-and-limits).
