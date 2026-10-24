# Named agents

A named agent is a child agent you define once and the main agent can hand a job to. The child gets its own conversation, its own instructions, and optionally a smaller tool set. It runs beside the agent that launched it, can exchange messages with it and with you, and reports its final answer when it ends. Use one for a contained job you want done the same way every time, such as a read-only reviewer, a test writer, or a dependency auditor.

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

A plain name means the custom agent when one exists and the core agent otherwise. `core:<name>` always means the core agent. This holds for `PEEN_AGENT` and for the `agent` field of `launchAgent`. The `agent` field of an [event handler](events.md#waking-an-idle-session) is only text added to the wake message, so write whatever name you want the model to read.

Startup fails when `PEEN_AGENT` is a `core:` name that does not exist or is disabled. Any plain name is resolved at each turn, so an agent that only one workspace defines works for that workspace. A turn in a workspace without it fails.

`PEEN_DISABLED_CORE_AGENTS` removes core agents. It takes a comma-separated list of core names without the prefix, or `all`. Unknown names stop startup. A disabled core agent is gone from the catalogue and from agent lookup. Startup also fails when `PEEN_AGENT` is a `core:` name that is disabled, which includes the default `core:general-purpose`, while a plain `PEEN_AGENT` name passes startup and fails at a turn whose workspace defines no such custom agent. To disable `general-purpose`, set `PEEN_AGENT` to a custom agent. See [core and custom skills and agents](harness.md#core-and-custom-skills-and-agents).

## Format

YAML frontmatter, then the agent's instructions:

```markdown
---
name: reviewer
description: Reviews shell scripts for safety and style. Read-only.
allowed-tools: listFiles, searchText, readFile
---
Read every .sh file in the workspace. Report problems as a short bullet list
with the file and line. Do not change anything.
```

| Field | Required | Rules |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits, and single hyphens. It must equal the file name without `.md`. |
| `description` | yes | What the agent is for. The main agent reads it to decide when to use it. |
| `allowed-tools` | no | One comma-separated string of tool names. Leave it out to give the agent every tool. |

The body is the agent's instructions and must not be empty. The YAML is strict. Every value is a string, and unknown keys make the file invalid. There is no `model` field, because a child always runs on the same model as the turn that launched it.

These are the tool names `allowed-tools` accepts:

`listFiles`, `searchText`, `readFile`, `writeFile`, `editFile`, `applyPatch`, `movePath`, `makeDirectory`, `removePath`, `changeDirectory`, `runCommand`, `listJobs`, `readJobOutput`, `waitJob`, `signalJob`, `useSkill`, `launchAgent`, `sendMessage`, `waitMessages`, `stopAgent`, `ask`, `searchExecutions`, `readExecution`

An empty item or a repeated name makes the file invalid when Peen loads it. A misspelled tool name is caught later, when the agent runs, and that run fails with "agent allowed-tools names unavailable tool". The restriction applies to the agent wherever it runs, including when it is the root agent.

`allowed-tools` limits what an agent can do in the workspace. The messaging tools `sendMessage`, `waitMessages`, `stopAgent`, and `ask` only reach other agents in the session, so every agent has them whether `allowed-tools` lists them or not. A read-only reviewer can still ask a question and still collect the answers of children it launched.

Every agent also has `searchExecutions` and `readExecution` for inspecting retained work in its own session. These reads do not change the workspace or resume paused work.

Session-worker agents also have `resumeExecution` for explicitly continuing retained work and `resolveExecutionActivity` for recording inspected outcomes of uncertain steps. These tools control execution state within the session and do not change the workspace tool allowlist.

## How the main agent sees them

Every turn the system prompt lists the named agents. Core agents come first under their `core:` names, then custom agents, each group sorted by name:

```text
Available named agents:
Core agents (address as core:<name>):
- core:general-purpose: General-purpose agent for coding and other repository tasks. (allowed tools: ; source: embedded://agents/general-purpose.md)
Custom agents (defined by you, no prefix):
- reviewer: Reviews shell scripts for safety and style. Read-only. (allowed tools: listFiles, searchText, readFile; source: /home/me/work/my-app/.agents/agents/reviewer.md)
```

An empty `allowed tools` list, as on `core:general-purpose`, means the agent can use every tool.

The main agent starts one with the `launchAgent` tool:

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

A call names either `agent` or `agentDefinition`, never both and never neither. Inline instructions are capped by `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` (64 KiB by default) and are never written to disk.

`launchAgent` returns as soon as the child runs. The tool result names the child, not its answer:

```json
{"agentRunId": "0b1c...", "name": "reviewer", "definition": "stored", "parentAgentRunId": "root"}
```

`parentAgentRunId` is the run ID of the agent that launched the child, or `root` for the session's root conversation. A session may have up to `PEEN_MAX_CONCURRENT_AGENT_RUNS` children running at once. A launch past that limit fails with "too many concurrent agent runs".

Paused session-worker children release their active slots after their callbacks stop. New work can use those slots without resuming the paused children. If an explicitly resumed child finds every slot occupied, it stays paused with its saved context and results. Resume it again after another child stops.

## Running children

A child runs on its own, beside the agent that launched it. The launcher keeps working, can start more children, and hears back when each one ends.

### How long a child runs

A child keeps running after the turn that launched it ends. It stops when it gives its final answer, when it runs out of tool rounds, or when something stops it:

- `POST /api/v1/session/cancel` and the Cancel button pause unfinished session workflows and interrupt their activities. Embedded sessions use the same pause behavior.
- `stopAgent`, `POST /api/v1/session/agents/{agentRunId}/cancel`, and the Stop button in the control surface stop one child.
- A child can keep running after its parent finishes. Its execution and final report remain stored in service and embedded sessions.
- Normal shutdown pauses unfinished work. After a crash, unfinished work is paused too, unless `PEEN_CRASH_RECOVERY` is `resume`.

### Recovery after a crash

Service and embedded sessions use one workflow definition with saved context and stable execution identities. Their workers restore children through workflow history.

After the controller or a worker crashes, Peen pauses every execution that was running, the root's and its children's, and ends the interrupted turn with a stored `turn.failed` frame. Nothing continues until a turn asks the agent to inspect and resume it, as [continuing retained work](#continuing-retained-work) describes. With `PEEN_CRASH_RECOVERY=resume`, for deployments that run unattended, the next worker continues that work by itself instead.

A resumed execution continues children and grandchildren with their original run IDs, ancestry, start times, directories and saved conversation. An interrupted model request retries with that context. Completed tool results are reused. A completed parent does not prevent its unfinished children from continuing. Recovery waits for an abandoned activity lease to expire before reclaiming its work. Paused children remain paused until something resumes them or [sends them a message](#messages-to-a-paused-child).

Restored conversations retain their task, instructions, model, tool selection, frozen harness, summary and accepted inbox messages. Recorded provider rounds still count against the original limit; increasing the deployment limit does not grant extra rounds. Pending `ask` and `waitMessages` calls keep their original deadlines and saved questions. Uncertain tool effects require inspection before retry or acceptance through the controls below.

Completed, failed and cancelled children stay ended. A cancellation requested before recovery prevents a restart. An embedded runtime starts workflow workers for its stored sessions at construction and for newly opened sessions. Closing it pauses unfinished work and drains its workers before closing storage.

See [durable state](architecture.md#durable-state-and-visibility) for storage and recovery details.

### Inspecting retained work

An agent can use `searchExecutions` to find earlier work, including work you stopped. `text` searches task text, saved execution context, pause reasons and failures as a literal substring. Optional filters are `id`, `parentId`, `rootId`, `state`, `fromTime` and `toTime`. Times use RFC 3339, with an inclusive start and exclusive end. The states are `queued`, `running`, `paused`, `reconciling`, `completed`, `failed` and `cancelled`. Results return metadata and ancestry, with `limit`, `offset` and `hasMore` for pagination.

`readExecution` takes an `executionId` from those results. Its optional `part` selects `execution` metadata, `activities` with step inputs and outputs, ordered `events`, or the accepted-message `inbox`. Each history part has its own `limit`, `offset` and `hasMore`. A page that exceeds the tool result budget returns a tool error. Request fewer records, or use the ordered events to find an activity and read its context in chunks.

Use `part: "context"` for the saved conversation. Without `activityId`, it reads the execution's launch or terminal snapshot. Supply an `activityId` from the activity history to read that step's intermediate checkpoint. Context pages count bytes, default to at most 4096 bytes, preserve UTF-8 and return `nextOffset`. Follow `nextOffset` until `hasMore` is false to reconstruct the saved JSON. Inspect the current workspace before deciding how to continue. Reading retained work leaves it paused.

### Continuing retained work

When you ask an agent to continue an earlier task, it can call `resumeExecution` with that task's `executionId` and the `controlRevision` returned by its latest inspection. Peen resumes paused members of that subtree and preserves completed, failed and cancelled outcomes. A newer Stop or changed target refuses a stale request. The execution history records the calling execution and tool attempt.

A message accepted after a root's completion boundary is retained in that root's inbox as deferred input. Peen starts it as a separate root after the current root finishes, or recovers it at worker startup. A later Stop leaves it stored. To continue one stopped message, inspect the completed source with `readExecution`, `part: "inbox"`, then call `resumeExecution` with that source's `executionId` and `controlRevision`. Copy the message's `requestId` and `stopRevision` into `deferredRequestId` and `deferredStopRevision`. Peen admits only that selected input and preserves an existing deferred turn's identity. It leaves the completed source and other stopped messages alone. The execution history records the selection and calling activity as `inbox.resumed`.

A resumed root waits while another turn owns the session, then continues after that turn finishes. A resumed child can run alongside the current turn, subject to the child capacity limit. Worker startup restores signals for admitted unfinished work and leaves paused work paused. Resuming does not resolve uncertain tool effects.

For an uncertain step, `resolveExecutionActivity` takes its `activityId`, saved `attempt`, the execution's inspected `controlRevision`, and `evidence` describing what the agent checked. `resolution: "retry"` permits another attempt when repeating the effect is safe. `resolution: "accept"` requires the complete inspected `output` and `context` checkpoint objects and retains that result without repeating the effect. The history records the decision and its caller. Resolution leaves paused work paused; continuing it still requires `resumeExecution`. Leave an outcome uncertain when inspection cannot establish what happened.

### Answers come back as messages

A child's final answer goes to the agent that launched it.

In session workers, a child question can also wake an idle root. Startup and root completion check retained pending questions, including ones published before a crash. Paused sources stay queued, and a configured queue-only event handler keeps holding the question. Reading or checking a question does not consume it; admitting the wake records its delivery with the new turn.

- A child of the root conversation reports through an `agent.finished` [session event](events.md#agent-events), or `agent.failed` when it did not complete. The event data carries the final answer in `response`. The root reads the event at its next tool call, or a new turn starts for it when the root is idle.
- A child of a child reports into its parent's conversation as a message, after the parent's current tool call. The message is `<message from="agent" name="<name>" run="<agentRunId>" kind="result">` with the final answer, or `kind="failed"` or `kind="cancelled"` with what stopped it.

`waitMessages` blocks until a message waits for the caller, then returns how many wait. For the root, pending session notices also count, including saved notices after a restart. Waiting leaves those notices pending until context delivery. The messages reach the conversation right after the tool result. A parent that launched children uses it to collect the answers it needs before giving its final answer. `waitMessages` takes an optional `timeout` as a Go duration, such as `"10m"`. Without one it waits until something arrives, for at most 30 days.

### Messages and sender tags

Every message an agent receives from another participant names its sender, so the agent knows who is talking:

```text
<message from="agent" name="core:general-purpose" run="root">
Review the shell scripts in this workspace.
</message>
```

A child's task is the first such message, from the agent that launched it. `from="agent"` carries the sender's agent name and run ID, or `root` for the root conversation. A message you send to a child arrives as `<message from="user">`. A message you send to the root conversation stays untagged, as it always was. The default system prompt tells the model what these blocks are.

`sendMessage` sends a message to another agent. `to` is `root` or the `agentRunId` of any running agent in the session, such as a child, or the parent named in the `run` attribute of the task. A message to a child or parent lands after its current tool call. A message to `root` from any child, at any depth, reaches the root as an `agent.message` session event. Sending to a paused child resumes it, as [described below](#messages-to-a-paused-child). Sending to a child that has ended fails and says how it ended. Sending to a run ID the session never had fails as unknown.

You can write to a running child yourself. Name its run ID in the `to` field of a WebSocket `message.send`, or pick it under Send to in the control surface. Session workers retain accepted messages while reconstructing a child's conversation. See [the WebSocket](http-api.md#send-and-watch-turns-over-websocket).

### Messages to a paused child

A message to a paused child, from you or from another agent, resumes that child and the paused work under it. The child first reads why it was paused, then the message:

```text
<paused>
<reason>turn workflow failed: call the model: upstream refused the request</reason>

Your work was paused for the reason above, and the message after this one resumes it. Check the workspace before you continue, because it may have changed while you were paused.
</paused>
```

The reason is the Stop, the crash, or the error the turn failed with. The execution history records the resume as `execution.resumed` with the message's `requestId`. A message that does not fit the child's inbox is refused and leaves the child paused.

### Errors reach every agent

When a turn fails, is interrupted, or is stopped, Peen records a [`turn.failed` or `turn.cancelled` session event](events.md#turn-events) with the error. The root reads it at its next tool call or turn. Every child that is queued or running gets a copy too. Each `agent.failed` event reaches them the same way. A child reads its copy before its next model request, in the same `<events>` block the root reads, and a full inbox does not hold it back. A paused child gets no copy, because the `<paused>` block tells it what stopped it.

### Questions

`ask` sends a question and waits for the next message, which is normally the answer. A child asks the agent that launched it. A child of the root asks through an `agent.question` session event, which starts a turn when the root is idle. A deeper child asks with a `<message kind="question">` in its parent's conversation. The root conversation asks you. The question shows above the composer, and your next message is the answer.

The asker answers with `sendMessage` to the asking run, and you can answer a child's question directly with a message whose `to` names it. While a child waits on a question, `GET /api/v1/session/agents` shows it with `waiting: true` and the `question`, and `?state=waiting` lists only those runs.

`PEEN_ASK_TIMEOUT` bounds how long `ask` waits, and a call's own `timeout` may shorten it. The default `0` sets no limit of its own, so `ask` waits up to the 30-day cap every messaging tool has, and a child whose question nobody answers holds one of the session's `PEEN_MAX_CONCURRENT_AGENT_RUNS` slots until someone answers or stops it. The root's `ask` still ends with its turn, at `PEEN_TURN_TIMEOUT`. A timed out `ask` returns `answered: false`. Interrupting a root or child wait leaves its recorded question open. Cancellation or a deadline interrupting delivery to the parent also leaves the child's question open; an ordinary delivery refusal closes it unanswered and returns the error.

### Stopping a child

`stopAgent` takes an `agentRunId` and stops that child and its descendants. Stopping a child that already ended reports its state. An agent cannot stop itself. It gives its final answer instead.

For session-worker workflows, stopping a child records `cancelled` on its unfinished subtree and activities before interrupting live work. Saved context and history remain available, late activity results are refused, and the parent receives one retained cancellation report. Completed descendants keep their outcomes. A replacement worker finishes any SDK cancellation that the previous worker did not receive. Session Stop keeps its separate pause behavior.

## What a child gets

A child shares the parent's session, workspace, rules, and model. Its system prompt is built in this order:

1. Peen's base prompt.
2. Every `AGENTS.md` and rule block.
3. The skill and named-agent catalogues, and any harness diagnostics.
4. The child's own instructions.
5. The workspace path and runtime facts.

It does not get the root agent's instructions or the message's `systemPrompt` override. It inherits the launching agent's current directory and harness. Its own `changeDirectory` calls reload its local harness without changing its parent or siblings. The launched agent's instructions and tool allowlist remain its own. Session events are delivered to the root agent only, never to a child. Its task arrives as a `<message>` block naming the agent that launched it.

A skill you forced on the parent turn with `:skill-name` does not reach the child. The parent hands a skill over by naming it in the `task`. The task is treated like a user message. Each `:name` in it, and each skill those skills name, arrives as its own `<skill>` message right after the task, in the child's first request and in its stored transcript. An unknown name fails the `launchAgent` call and lists up to 10 of the skills that exist. See [Forcing a skill](skills.md#forcing-a-skill).

## Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `5` | How deep children may nest. A child at the limit does not get `launchAgent`. |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `16` | Tool rounds one child may run. |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `4` | Children one session may run at the same time. |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `65536` | Size cap on inline instructions. |
| `PEEN_ASK_TIMEOUT` | `0` | How long `ask` waits for an answer. `0` waits up to the 30-day cap of the messaging tools. |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `16` | Messages one child's inbox holds before a message to it is refused, the same bound the root's queue has. |

At most 64 named agents across all layers. See [the harness limits](harness.md#mistakes-and-limits).

## Watching a child

Every child run is stored in SQLite with its own messages, events, and compactions. A message a child received from another agent or from you is part of its stored transcript from the moment it reached the model. The control surface shows the running children above the composer, with the question each one waits on and a Stop button, and lists every run in the details panel. The API has `GET /api/v1/session/agents`, which filters by `state`, including `waiting`, and by `parentAgentRunId`. It also has `GET /api/v1/session/agents/{agentRunId}/messages`, `GET /api/v1/session/agents/{agentRunId}/events`, and `POST /api/v1/session/agents/{agentRunId}/cancel`. See the [API reference](http-api.md#get-apiv1sessionagents).

When a child of the root conversation ends, Peen publishes an `agent.finished` or `agent.failed` [session event](events.md#agent-events) with the run ID, agent name, state, duration, and the final answer. That event is how the root hears the answer. A deeper child reports into its parent's conversation instead and publishes no session event.
