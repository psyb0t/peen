# Skills

A skill is a named procedure the agent loads only when it needs it. Every turn the model sees a short catalogue with each skill's name and description. When a task matches, it calls `use_skill` to load the full instructions. You can also force a skill with `:skill-name` in your message. Use a skill for a procedure that is too long or too specific to sit in the [rules](rules.md) on every turn, such as cutting a release, writing a migration, or triaging a bug report.

Peen follows the [Agent Skills](https://agentskills.io) layout, so a skill written for Claude Code works here unchanged.

## Where skills go

A skill is a directory with a `SKILL.md` inside it, in any [harness layer](harness.md#where-peen-looks):

```text
my-app/
  .agents/
    skills/
      release-notes/
        SKILL.md
        references/
          style.md
        scripts/
          collect-changes.sh
```

Peen reads `.claude/skills/<name>/SKILL.md` and `.agents/skills/<name>/SKILL.md`. Every entry in a `skills/` directory must be a directory. A stray file there is reported as "skill entry is not a directory".

Peen ships two skills of its own, `planning` and `freshness`. Define a skill with the same name to replace one.

## Format

`SKILL.md` is YAML frontmatter followed by the instructions. The frontmatter must start on the first line with `---` and end with another `---`. The body after it must not be empty.

```markdown
---
name: release-notes
description: Write RELEASE_NOTES.md from the files and changes in this workspace.
---
# Release notes

1. Run `scripts/collect-changes.sh` from this skill's directory.
2. Read `references/style.md` for the tone.
3. Write `RELEASE_NOTES.md` with one short line per change.
```

| Field | Required | Rules |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits, and single hyphens, up to 64 characters. It must equal the directory name. |
| `description` | yes | Up to 1024 characters. This is what the model reads to decide whether to load the skill, so say when to use it. |
| `license` | no | String. |
| `compatibility` | no | String of 1 to 500 characters. |
| `homepage` | no | String. |
| `user-invocable` | no | Boolean. |
| `metadata` | no | Mapping, nested values allowed. |
| `permissions` | no | Mapping. Recorded only. |
| `allowed-tools` | no | String. Recorded only. Peen does not restrict tools for a skill. Use a [named agent](agents.md) when a job needs a narrower tool set. |

The YAML is strict. An unknown key, a second YAML document, or a non-string value where a string is expected makes the skill invalid. Peen then skips that skill, keeps everything else, and shows a workspace configuration warning in the chat.

## How the model finds a skill

Every turn the system prompt carries the catalogue, sorted by name:

```text
Available skills:
- freshness: Verify time-sensitive facts with current authoritative evidence before using them. (embedded://skills/freshness/SKILL.md)
- planning: Create evidence-based implementation and test plans before nontrivial work. (embedded://skills/planning/SKILL.md)
- release-notes: Write RELEASE_NOTES.md from the files and changes in this workspace. (/home/me/work/my-app/.agents/skills/release-notes/SKILL.md)
```

Only the names and descriptions are in the prompt. The model calls `use_skill` with `{"name": "release-notes"}` and gets back the full `SKILL.md`, the skill's absolute directory, and a content hash. Files next to `SKILL.md`, such as `references/` and `scripts/`, are not loaded automatically. The model reads them with `read_file` and runs scripts with `run_command` in that directory, so each one shows up as its own tool call in the chat. An unknown name returns an error that lists the skills that do exist.

## Forcing a skill

Write `:skill-name` at the start of a message or after whitespace to require that skill for the turn:

```text
:release-notes write the notes for this week
```

The name must end at the end of the message, whitespace, or one of `, . ; ! ? ) ] }`. Peen checks the name before the turn opens. An unknown or invalid skill fails the message with `message.failed` and no model call is made. A known skill has its whole `SKILL.md` placed in the system prompt, for the root agent and every child agent of that turn. Your message is stored exactly as you typed it.

A message queued into a turn that is already running cannot force a skill, because that turn's prompt is already fixed.

## When skills load

Peen reads every skill at the start of every turn, so a new or edited skill is available on the next message. During a turn the skills are frozen: `use_skill` returns the content Peen read when the turn started.

## How skills combine

Skills replace by name and are never merged. A skill in a deeper layer replaces one with the same name from a broader layer. Inside one layer, `.agents/skills/<name>` replaces `.claude/skills/<name>`. The catalogue and `use_skill` always use the winning copy, and `use_skill` returns that copy's directory, so bundled scripts come from the same place.

## Limits

At most 64 skills across all layers, 128 KiB per file, and 1 MiB for the whole harness. See [the harness limits](harness.md#mistakes-and-limits).
