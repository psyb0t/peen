# Named agents

A named agent is a child agent you define once and the main agent can hand a job to. The child gets its own conversation, its own instructions, and optionally a smaller tool set, then reports one final answer back. Use one for a contained job you want done the same way every time, such as a read-only reviewer, a test writer, or a dependency auditor.

## Where agents go

One Markdown file per agent, in any [harness layer](harness.md#where-peen-looks):

```text
my-app/
  .agents/
    agents/
      reviewer.md
      test-writer.md
```

Only `.agents/agents/<name>.md` is read. There is no `.claude/agents` path.

Peen ships one core agent, `general-purpose`, addressed as `core:general-purpose`. It is the root agent every turn runs as, unless `PEEN_AGENT` names another one. Your agents are custom agents. They sit beside the core agent and never replace it, even when a custom agent is also named `general-purpose`.

A plain name means the custom agent when one exists and the core agent otherwise. `core:<name>` always means the core agent. This holds for `PEEN_AGENT` and for the `agent` field of `launch_agent`. The `agent` field of an [event handler](events.md#waking-an-idle-session) is only text added to the wake message, so write whatever name you want the model to read.

Startup fails when `PEEN_AGENT` is a `core:` name that does not exist or is disabled. The old embedded name `default` no longer exists: `PEEN_AGENT=default` fails with an error naming `core:general-purpose` unless a `default.md` agent exists in `PEEN_CONFIG_DIR`. Drop `PEEN_AGENT` or set it to `core:general-purpose`. Any other plain name is resolved at each turn, so an agent that only one workspace defines works for that workspace. A turn in a workspace without it fails.

`PEEN_DISABLED_CORE_AGENTS` removes core agents. It takes a comma-separated list of core names without the prefix, or `all`. Unknown names stop startup. A disabled core agent is gone from the catalogue and from agent lookup. Startup also fails when `PEEN_AGENT` is a `core:` name that is disabled, which includes the default `core:general-purpose`, while a plain `PEEN_AGENT` name passes startup and fails at a turn whose workspace defines no such custom agent. To disable `general-purpose`, set `PEEN_AGENT` to a custom agent. See [core and custom skills and agents](harness.md#core-and-custom-skills-and-agents).

## Format

YAML frontmatter, then the agent's instructions:

```markdown
---
name: reviewer
description: Reviews shell scripts for safety and style. Read-only.
allowed-tools: list_files, search_text, read_file
---
Read every .sh file in the workspace. Report problems as a short bullet list
with the file and line. Do not change anything.
```

| Field | Required | Rules |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits, and single hyphens. It must equal the file name without `.md`. |
| `description` | yes | What the agent is for. The main agent reads it to decide when to use it. |
| `allowed-tools` | no | One comma-separated string of tool names. Leave it out to give the agent every tool. |

The body is the agent's instructions and must not be empty. The YAML is strict: every value is a string, and unknown keys make the file invalid. There is no `model` field, because a child always runs on the same model as the turn that launched it.

These are the tool names `allowed-tools` accepts:

`list_files`, `search_text`, `read_file`, `write_file`, `edit_file`, `apply_patch`, `move_path`, `make_directory`, `remove_path`, `run_command`, `list_jobs`, `read_job_output`, `wait_job`, `signal_job`, `use_skill`, `launch_agent`

An empty item or a repeated name makes the file invalid when Peen loads it. A misspelled tool name is caught later, when the agent runs, and that run fails with "agent allowed-tools names unavailable tool". The restriction applies to the agent wherever it runs, including when it is the root agent.

## How the main agent sees them

Every turn the system prompt lists the named agents. Core agents come first under their `core:` names, then custom agents, each group sorted by name:

```text
Available named agents:
Core agents (address as core:<name>):
- core:general-purpose: General-purpose agent for coding and other repository tasks. (allowed tools: ; source: embedded://agents/general-purpose.md)
Custom agents (defined by you, no prefix):
- reviewer: Reviews shell scripts for safety and style. Read-only. (allowed tools: list_files, search_text, read_file; source: /home/me/work/my-app/.agents/agents/reviewer.md)
```

The main agent starts one with the `launch_agent` tool:

```json
{"task": "Review the shell scripts in this workspace.", "agent": "reviewer"}
```

It can also define a one-off child inline when no stored agent fits:

```json
{
  "task": "Check the Dockerfile for pinned base images.",
  "agentDefinition": {
    "name": "image-checker",
    "instructions": "Read Dockerfile and report every FROM line without a digest."
  }
}
```

Exactly one of `agent` and `agentDefinition` must be present. Inline instructions are capped by `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` (64 KiB by default) and are never written to disk.

`launch_agent` waits for the child and returns its final answer as the tool result. A session may have up to `PEEN_MAX_CONCURRENT_AGENT_RUNS` children running at once. A launch past that limit fails with "too many concurrent agent runs".

## What a child gets

A child shares the parent's session, workspace, rules, and model. Its system prompt is built in this order:

1. Peen's base prompt.
2. Every `AGENTS.md` and rule block.
3. The skill and named-agent catalogues, and any harness diagnostics.
4. The child's own instructions.
5. The workspace path and runtime facts.

It does not get the root agent's instructions or the message's `systemPrompt` override. It uses the harness snapshot of the turn that launched it. Session events are delivered to the root agent only, never to a child.

A skill you forced on the parent turn with `:skill-name` does not reach the child. The parent hands a skill over by naming it in the `task`. The task is treated like a user message: each `:name` in it, and each skill those skills name, arrives as its own `<skill>` message right after the task, in the child's first request and in its stored transcript. An unknown name fails the `launch_agent` call and lists up to 10 of the skills that exist. See [Forcing a skill](skills.md#forcing-a-skill).

## Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `5` | How deep children may nest. A child at the limit does not get `launch_agent`. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Children one session may run at the same time. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size cap on inline instructions. |

At most 64 named agents across all layers. See [the harness limits](harness.md#mistakes-and-limits).

## Watching a child

Every child run is stored in SQLite with its own messages, events, and compactions. The control surface lists runs in the details panel. The API has `GET /v1/session/agents`, `GET /v1/session/agents/{agentRunId}/messages`, `GET /v1/session/agents/{agentRunId}/events`, and `POST /v1/session/agents/{agentRunId}/cancel`. See the [API reference](http-api.md#get-v1sessionagents).

When a child finishes, Peen publishes an `agent.finished` or `agent.failed` [session event](events.md) with the run ID, agent name, and duration. It does this for every run, even though `launch_agent` already returned the child's answer to the parent. For the model the event repeats what it already knows. It exists as a durable record and as something an event handler can react to.
