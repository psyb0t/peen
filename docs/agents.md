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

Peen ships one agent, `default`. It is the root agent every turn runs as, unless `PEEN_AGENT` names another one. Define `.agents/agents/default.md` to replace its instructions for a workspace.

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

Every turn the system prompt lists the named agents, sorted by name:

```text
Available named agents:
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
2. Every rule block.
3. The skill and named-agent catalogues, and any harness diagnostics.
4. Any skill the user forced with `:skill-name` for this turn.
5. The child's own instructions.
6. The workspace path and runtime facts.

It does not get the root agent's instructions or the message's `systemPrompt` override. It uses the harness snapshot of the turn that launched it. Session events are delivered to the root agent only, never to a child.

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

When a child finishes, Peen publishes an `agent.finished` or `agent.failed` [session event](events.md) with the run ID, agent name, and duration.
