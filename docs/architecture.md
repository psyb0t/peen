# Architecture

Peen is a durable coding-agent service. A client opens a WebSocket for a
session, sends a task, and watches the model, tools, hooks, and child agents
work. The same session can have several connected clients. Peen saves the work
as it happens, so reconnecting after a client or process restart does not throw
the conversation away.

```text
WebSocket client
      |
      v
HTTP server and session hub
      |
      v
agent runtime <--> model provider
      |                  |
      |                  v
      |              model events
      v
harness, tools, hooks, child agents
      |
      +--> workspace files and commands
      +--> SQLite sessions, messages, events, and snapshots
      +--> WebSocket clients
```

REST sits beside the WebSocket. It reads durable session state, lists messages,
events, jobs, and child-agent runs, or cancels work. It never accepts a user
task or starts a turn. [The API reference](http-api.md) has the contract.

## One turn

The runtime resolves the workspace and its harness layers first. It builds the
model context from the conversation, project rules, available skill and agent
metadata, and current runtime facts. The provider may then ask to use tools.
Peen runs those tools, applies matching hooks, records the result, and returns
it to the provider until the turn completes or fails.

File mutations have a deliberate safety rule. A turn must read an existing file
before it can change that file. Peen checks the observed hash again immediately
before the mutation. New destinations must not already exist. These checks stop
stale or blind writes from silently replacing a file.

## The harness

`AGENTS.md` carries project rules. `.agents/skills` advertises named procedures
that the model can load when needed. `.agents/agents` defines bounded child
agents. `.agents/events` tells Peen how to wake a session for an external event.
`.agents/hooks.yaml` adds mechanical actions around lifecycle and tool events.

Peen applies the configuration directory first, then filesystem layers from the
root down to the active workspace. Rules closer to the file being worked on are
therefore more specific. [Configuration](configuration.md#harness-layering) and
[hooks](hooks.md) describe the exact rules.

## Durable state and visibility

SQLite is the source of truth for sessions, turns, messages, events, prompt
snapshots, and compactions. The agent runtime also keeps child-agent JSONL
mirrors for tailing. Structured logs go to stdout and daily audit files. The
audit log records safe identifiers and digests. The transcript holds the
verbatim data, so keep its storage and every connected client as protected as
the workspace itself.

## Process lifecycle

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for process and
service lifecycle plumbing. Peen owns the agent behavior, public API, storage,
and harness. Servicepack's framework details live in its own repository.
