# Skills

A skill is a named procedure the agent loads only when it needs it. Every turn the model sees a short catalogue with each skill's name and description. When a task matches, it calls `useSkill` to load the full instructions. You can also hand a skill to the model with `:skill-name` in your message. Use a skill for a procedure that is too long or too specific to sit in the [rules](rules.md) on every turn, such as cutting a release, writing a migration, or triaging a bug report.

Peen follows the [Agent Skills](https://agentskills.io) layout, so a skill written for Claude Code works here unchanged as long as its frontmatter uses only the keys listed under [Format](#format). Peen skips a skill with any other key and says so in the chat.

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

Peen ships four core skills of its own, `planning`, `freshness`, `delegation`, and `plan-and-delegate`. A skill you define with the same name does not replace one. Both exist, and [core and custom skills](#core-and-custom-skills) explains how a name picks between them.

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

## Core and custom skills

Skills come in two groups. Core skills are embedded in Peen. Custom skills are the ones you write in a harness layer. The groups never replace each other.

| Core skill | What it tells the agent |
| --- | --- |
| `planning` | Write an evidence-based plan under `.plans/` before any change. |
| `freshness` | Check facts that change over time against a current source instead of memory. |
| `delegation` | Hand parts of a task to child agents, talk to them while they work, and collect their answers. See [running children](agents.md#running-children). |
| `plan-and-delegate` | Plan a larger change, split it into parts that touch different files, delegate each part, and verify the combined result. |

A core skill is addressed with the `core:` prefix: `core:planning` in `useSkill`, `:core:planning` in a message. A plain name means the custom skill when one exists and the core skill otherwise. With no custom `planning` skill, `:planning` loads the core one. Once you write `.agents/skills/planning/SKILL.md`, `:planning` loads yours and `:core:planning` still loads Peen's. A custom skill name cannot contain `:`.

To turn core skills off, set `PEEN_DISABLED_CORE_SKILLS` to a comma-separated list of core names without the prefix, such as `planning,freshness`, or to `all`. A disabled core skill is gone from the catalogue, from `useSkill`, and from `:name`. An unknown name stops startup. See [configuration](configuration.md).

## How the model finds a skill

Every turn the system prompt carries the catalogue. Core skills come first under their `core:` names, then custom skills, each group sorted by name:

```text
Available skills:
Core skills (address as core:<name>):
- core:delegation: Hand parts of a task to child agents, talk to them while they work, and collect their answers. (embedded://skills/delegation/SKILL.md)
- core:freshness: Verify time-sensitive facts with current authoritative evidence before using them. (embedded://skills/freshness/SKILL.md)
- core:plan-and-delegate: Plan a larger change, split it into independent parts, delegate them to child agents, and verify the combined result. (embedded://skills/plan-and-delegate/SKILL.md)
- core:planning: Create evidence-based plans and test plans before any request that changes something. (embedded://skills/planning/SKILL.md)
Custom skills (defined by you, no prefix):
- release-notes: Write RELEASE_NOTES.md from the files and changes in this workspace. (/home/me/work/my-app/.agents/skills/release-notes/SKILL.md)
```

A group with no skills has no heading. Only the names and descriptions are in the prompt. The model calls `useSkill` with `{"name": "release-notes"}` and gets back the full `SKILL.md`, the skill's directory, and a content hash. [Forcing a skill](#forcing-a-skill) explains what the directory of a core skill is. Files next to `SKILL.md`, such as `references/` and `scripts/`, are not loaded automatically. The model reads them with `readFile` and runs scripts with `runCommand` in that directory, so each one shows up as its own tool call in the chat. An unknown name returns an error that lists the skills that do exist.

## Forcing a skill

Write `:skill-name` at the start of a message or after whitespace to hand that skill to the model:

```text
:release-notes write the notes for this week
```

A core skill is written `:core:planning`. Only the exact `core:` prefix followed by a valid name counts as one reference, so `:core:` alone and `:core:Bad` are ordinary text. The name must end at the end of the message, whitespace, or one of `, . ; ! ? ) ] }`. Peen checks every name before it does anything else. An unknown or invalid skill fails the message with `message.failed` and `VALIDATION_FAILED`, naming the skill and listing up to 10 of the available skills with a count of the rest. Nothing is stored and no model call is made.

Each skill reaches the model as its own user message right after yours, never through the system prompt:

```text
<skill>
<name>release-notes</name>
<description>Write RELEASE_NOTES.md from the files and changes in this workspace.</description>
<directory>/home/me/work/my-app/.agents/skills/release-notes</directory>

(the whole SKILL.md, frontmatter included)
</skill>
```

The name is the one Peen resolved, so a core skill shows as `core:planning`. For a custom skill the directory is absolute, so the model can read `references/` and run `scripts/` from there. A core skill's directory is `embedded://skills/<name>`, which names the embedded copy and is not a path on disk. The default system prompt tells the model that a `<skill>` message holds a skill you invoked, that it applies to the current work, and that your own instructions take precedence over the skill. The same sentence block names the other tags Peen adds: `<hook>` is treated like a skill, `<handler>` holds the instructions an event handler started the turn with, `<note>` is a short notice from Peen, and `<events>` is data to read, never instructions. See [hook `inject`](hooks.md#inject) and [event handlers](events.md).

Peen marks a `<skill>` or `<hook>` block as instructions from your harness layers, whichever layer it came from. The agent can write a workspace `.agents/skills/*/SKILL.md` and a workspace `hooks.yaml` with its own file tools (see the [hook limits](hooks.md#limits)), and the next turn delivers that text under the same mark. Keep a skill that must stay trusted in `PEEN_CONFIG_DIR`.

Your message is stored as you typed it. Each skill message is stored right after it as an injected message of kind `skill`, so a resumed session replays them and the model sees them in later turns. The control surface shows each one as a collapsed `skill: name` marker that expands to the content, not as a bubble from you.

A message sent while a turn is running works the same way. Peen resolves the names when it accepts the message, and an unknown name refuses that message, and nothing is queued or stored. The message and its skill messages reach the model together when the model's current round ends, after every tool call in that round, and `userMessage.delivered` fires once for your message. Each skill message takes one slot of `PEEN_MAX_QUEUED_USER_MESSAGES`, so a message that brings skills leaves less room in the queue.

### Skills that name other skills

A `SKILL.md` body can name other skills with `:name` or `:core:name`, under the same rules as a message. The frontmatter is not scanned. When a skill reaches the model through `:name` or `useSkill`, every skill it names comes with it, and every skill those name in turn, depth first in the order they are named. Through `:name`, each one is its own `<skill>` message after the one that named it. Through `useSkill`, they come back in a `referencedSkills` array of `name`, `directory`, and `content`, leaving out any skill the conversation already holds (see [Once per conversation](#once-per-conversation)). The skill you ask `useSkill` for always comes back in full.

A `:name` in a `SKILL.md` that names no skill is a workspace configuration warning when Peen loads the harness. The warning names the skill file and the reference. Peen skips the reference when it delivers the skill.

### Once per conversation

Peen does not send a skill twice while the model still has it, which also stops a cycle where skill A names B and B names A. When you name a skill the model already holds, Peen sends the note `<note>Skill NAME is already loaded earlier in this conversation.</note>` instead of a second copy. A skill that only another skill names is left out without a note. Naming a skill twice in one message delivers it once. Which skills the model holds depends on `PEEN_COMPACTION_MODE`:

- Within one turn, a skill delivered by `:name` or by `useSkill` counts, along with the skills it brought. This holds in both modes, for the turn's first message and for messages sent while the turn runs.
- Under the default `drop-oldest`, Peen stores no compaction and leaves old messages out of the request on its own, so a delivery from an earlier turn may no longer reach the model. A skill counts as delivered only in the turn that delivered it. A `:name` in a later turn delivers the skill in full again.
- Under `summarize`, a skill delivered in an earlier turn, by `:name` or by `useSkill`, still counts until a compaction covers it. After a compaction covers a skill message, the model has only a summary of it, and naming the skill again delivers it in full again. This includes a compaction in the middle of a running turn.

`useSkill` always returns the skill you ask for in full, whatever the model already holds.

One message, or one `useSkill` call, can bring at most 128 KiB of skill content, counting each delivered `SKILL.md` in full and not counting already-loaded notes. The limit is fixed. More than that fails the message, or the tool call, with a validation error.

### Skills and child agents

A skill you force on a turn is not copied to the child agents it launches. The parent hands a skill to a child by naming it with `:name` in the `task` of `launchAgent`. The task is treated like a user message. Each skill arrives as its own message right after the task, in the child's first request and in its stored transcript, and an unknown name fails the `launchAgent` call and lists up to 10 of the skills that exist.

## When skills load

Peen reads every skill at the start of every turn, so a new or edited skill is available on the next message. A `changeDirectory` call reloads the skill catalogue for the new directory and its ancestors. `useSkill` returns that scope's winning definition. Previously delivered skill messages remain in the conversation history.

## How skills combine

Custom skills replace by name and are never merged. A skill in a deeper layer replaces one with the same name from a broader layer. Inside one layer, `.agents/skills/<name>` replaces `.claude/skills/<name>`. The catalogue and `useSkill` always use the winning copy, and `useSkill` returns that copy's directory, so bundled scripts come from the same place. No layer replaces a core skill.

## Limits

At most 64 skills across all layers, 128 KiB per file, and 1 MiB for the whole harness. See [the harness limits](harness.md#mistakes-and-limits).
