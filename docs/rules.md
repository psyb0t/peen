# AGENTS.md and rules

Peen gives the model two kinds of plain Markdown:

- `AGENTS.md` is a project's instructions file, in the open [agents.md](https://agents.md) format that most coding agents read. It is the README for agents: how to build and test, where things live, what not to touch, and the project's conventions. It is in the system prompt on every turn.
- Rules are topic files in `.agents/rules/` or `.claude/rules/`, one subject per file, such as `testing.md` or `go.md`. Use them when one `AGENTS.md` would grow too long, or to keep a convention in its own file. A rule is in the system prompt on every turn, unless its frontmatter scopes it to certain files. Then it reaches the model only when the agent works on one of those files.

If something only matters for one kind of task, make it a [skill](skills.md). If it must be enforced rather than requested, use a [hook](hooks.md).

## AGENTS.md

An `AGENTS.md` is ordinary Markdown with no frontmatter and no required headings. Write it the way you would brief a new teammate.

```markdown
# my-api

- Build with `make build`. Run tests with `make test` before you finish.
- Every shell script starts with `#!/bin/bash`, a comment saying what it does, and `set -eu`.
- Never edit files under `vendor/`.
```

Peen reads `AGENTS.md` from these places:

| Where | What it covers |
| --- | --- |
| `PEEN_CONFIG_DIR/AGENTS.md` | Every workspace this controller opens |
| Every directory from `/` down to the current directory | The current directory, so `~/work/AGENTS.md` covers every project under `~/work/` |

A typical layout:

```text
/home/me/work/
  AGENTS.md                  # every project under work/
  my-api/
    AGENTS.md                # the my-api workspace
    web/
      AGENTS.md              # files under my-api/web/
```

When you open `/home/me/work/my-api`, the model gets `work/AGENTS.md`, then `my-api/AGENTS.md`. Calling `changeDirectory` with `{"directory":"web"}` adds `web/AGENTS.md` and every other harness piece in that directory. Returning to the parent removes the web layer.

When two `AGENTS.md` files disagree, the one closest to the file being changed wins, as the agents.md format specifies. Peen's own operating instructions tell the model this, and they tell it that your messages in the chat override every `AGENTS.md`.

Peen does not search descendant directories. Enter one with `changeDirectory` to load its instructions.

## Rules

A rule is one topic in its own Markdown file.

| File | Use it for |
| --- | --- |
| `.agents/rules/<name>.md` | One topic per file, such as `testing.md` or `go.md` |
| `.claude/rules/<name>.md` | The same, for projects that already keep Claude Code rules here |

Peen reads rule directories from the config directory and from every directory between `/` and the current directory, the same layers as `AGENTS.md`. A directory hop reloads those layers. Every `.md` file under a `rules/` directory counts, including files in subdirectories such as `rules/frontend/react.md`, up to 8 directories deep. Other files are ignored. `CLAUDE.md` is not read.

```text
/home/me/work/my-api/
  AGENTS.md
  .agents/
    rules/
      go.md
      database.md
      frontend/
        react.md
```

A rule's name is its path under `rules/` without `.md`, such as `go` or `frontend/react`. The name shows up in the context snapshot and in the block a scoped rule reaches the model as.

## Rules for certain files

A rule can start with YAML frontmatter that names the files it covers. Peen then leaves it out of the system prompt and gives it to the model when the agent works on a matching file. Claude Code uses this format for `.claude/rules/`, so those rule files work unchanged.

```markdown
---
paths:
  - "**/*.go"
  - "cmd/**/*.{go,mod}"
---

# Go

- Run `gofmt -w` on every file you change.
- Wrap errors with context: `fmt.Errorf("open config: %w", err)`.
```

`paths` takes a YAML list or one string of comma-separated patterns. Peen also reads the field names other agents use, so a file shared with them needs no second copy:

| Field | From | Meaning in Peen |
| --- | --- | --- |
| `paths` | Claude Code | The patterns |
| `globs` | Cursor | The same as `paths` |
| `applyTo` | GitHub Copilot | The same as `paths` |
| `alwaysApply: true` | Cursor | Load on every turn, whatever the patterns say |

Peen ignores every other field, such as Cursor's `description`. It strips the frontmatter before the model sees the rule, whether or not the frontmatter scopes it.

Patterns are relative to the directory that holds the rule's `.agents` or `.claude` directory. A rule in `PEEN_CONFIG_DIR` matches relative to the agent's current directory. A file outside that base never matches.

| Pattern | Matches |
| --- | --- |
| `*.md` | Markdown files in the base directory only |
| `**/*.go` | Go files in any directory, the base included |
| `src/**/*` | Every file under `src/` |
| `src/*.{ts,tsx}` | `.ts` and `.tsx` files directly in `src/` |
| `file[0-9].txt` | `file0.txt` to `file9.txt` |
| `photos \[2024/**` | Files under `photos [2024/`, with the `[` escaped |

`*` and `?` never match `/`. `[!abc]` or `[^abc]` matches one character outside the set. Inside a comma-separated string, a comma within `{...}` belongs to the brace group, so `"src/*.{ts,tsx}, lib/*.ts"` is two patterns. A pattern that does not parse, such as one with an unclosed `[`, matches nothing. Peen warns about it, and the rule keeps its other patterns.

When the frontmatter is not valid YAML, or `paths` is neither a string nor a list of strings, Peen warns and loads the rule on every turn, as Claude Code does. Cursor writes unquoted patterns such as `globs: *.ts,*.tsx`, which YAML cannot parse, so before giving up Peen reads plain `paths:`, `globs:`, `applyTo:`, and `alwaysApply:` lines from the block.

### How a scoped rule reaches the model

A scoped rule loads when `readFile`, `writeFile`, `editFile`, `applyPatch`, or `movePath` touches a matching file. It follows the call's result:

```text
<rule name="go" source="/home/me/work/my-api/.agents/rules/go.md" paths="**/*.go, cmd/**/*.{go,mod}">
# Go

- Run `gofmt -w` on every file you change.
...
</rule>
```

The block is a user-role message right after the tool result, after any `<hook>` block from the same call and before any `<events>` block. The default system prompt tells the model that a `<rule>` block is a trusted rule from your configuration for the files its `paths` attribute names.

- A rule loads once. While a block for it is still in the conversation, and less than half of `PEEN_MAX_CONTEXT_TOKENS` minus `PEEN_OUTPUT_RESERVE_TOKENS` has been added after it, Peen does not send it again.
- It comes back once the model may no longer have it. After `summarize` compaction replaces the turn that held it, the next matching call loads it again. `drop-oldest` leaves old history out of each request only once the conversation passes the budget, so a block more than half the budget back may be gone, and the next matching call loads it again. The same distance sends a rule again in a long context, where text far back gets less of the model's attention.
- When several calls in one round match the same rule, the first call in the model's order loads it.
- A child agent has its own conversation and loads its own copy.
- `listFiles`, `searchText`, `removePath`, `makeDirectory`, and `runCommand` load nothing, even when a command reads a matching file. Claude Code also loads rules for a `cat` of one file, but Peen does not guess which files a shell command touches.
- A call a hook denied loads nothing.

The agent reads an existing file before it changes it, so a rule usually arrives before the first edit of a matching file. A new file gets the rule after its first `writeFile`. To have the rule shape that first write too, add a [hook that denies the first write](hooks.md#example-hand-the-model-a-rule-before-the-first-write) with the rule as its reason.

## How they combine

Everything adds. Each `AGENTS.md` and unscoped rule becomes its own block in the system prompt, in this order: Peen's embedded operating instructions, then `PEEN_CONFIG_DIR`, then each directory from `/` down to the current directory, parents before children. Inside one layer the order is `AGENTS.md`, sorted `.claude/rules/` files, then sorted `.agents/rules/` files, with a subdirectory's files where its name sorts. A later file never replaces an earlier one. When a rule and an `AGENTS.md` conflict, put the more specific one in the deeper layer and say that it overrides the general one. Scoped rules that one call matches arrive in the same order.

A user message cannot rewrite these blocks. A message's `systemPrompt` only changes Peen's base prompt for that turn.

An empty or whitespace-only file, or one that holds nothing but frontmatter, is invalid and is skipped with a warning.

To run without Peen's embedded operating instructions, set `PEEN_DISABLE_CORE_INSTRUCTIONS=true`, or set `PEEN_BLANK_AGENT=true` to remove all of Peen's built-in text at once. See [a blank agent](harness.md#a-blank-agent).

## When they load

Peen reads every `AGENTS.md` and rule file at the start of every turn. Edit one, send the next message, and the model sees the new text. A running agent reloads the harness when it calls `changeDirectory`, including `{"directory":"."}` to refresh the same directory.

## A starter rule pack

[`examples/rules/`](../examples/rules/) holds short rules for Go, Bash, Python, TypeScript and JavaScript, Dockerfiles, SQL, Markdown, tests, and security. Each one is scoped with `paths`, except the security rule, which applies on every turn. They follow each language's published style guide and standard linters, and each tells the agent to follow the project's own configuration first. Peen does not load them by default. Copy the ones you want into `PEEN_CONFIG_DIR/.agents/rules/` to cover every workspace, or into one project's `.agents/rules/`:

```bash
cp examples/rules/go.md examples/rules/security.md "$PEEN_CONFIG_DIR/.agents/rules/"
```

## Check what the model got

Every turn stores a context snapshot listing each source file and its hash, and a prompt snapshot with the exact system prompt. Read them through `GET /api/v1/session/context-snapshots/{contextHash}` and `GET /api/v1/session/prompt-snapshots/{promptHash}` in the [API](http-api.md). A file that was skipped shows up as a workspace configuration warning in the chat and as a `harness.warning` event.

## Limits

At most 64 `AGENTS.md` and rule files together, 128 KiB per file, and 1 MiB for the whole harness. Going over a limit at turn start fails the turn. Going over a limit during a directory hop fails that tool call and keeps the prior directory and harness. See [the harness limits](harness.md#mistakes-and-limits).
