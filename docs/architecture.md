# Architecture

Peen is a durable coding-agent service. A client opens a WebSocket, sends a
task for a named session, and watches the model, tools, hooks, and child agents
work. The default socket is a global event feed. Clients build session tabs by
filtering event metadata, or request one server-side session filter. Peen saves
the work as it happens, so reconnecting after a client or process restart does
not throw the conversation away.

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
      +--> SQLite replay ledger
      +--> WebSocket clients
```

REST sits beside the WebSocket. It reads durable session state, lists messages,
protocol events, notices, compactions, model exchanges, jobs, and child-agent
runs, or cancels work. It never accepts a user task or starts a turn. [The API
reference](http-api.md) has the contract.

[Aichteeteapee](https://github.com/psyb0t/aichteeteapee) supplies Peen's HTTP
server, REST error envelope, and WShub WebSocket fan-out. Peen gives every
socket its own server-controlled WShub identity. The server fans a session's
events to global sockets and to sockets filtered for that session.

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

[SQLite](https://sqlite.org/) is the sole source of truth for sessions, turns,
messages, protocol events, context and prompt snapshots, compactions,
child-agent runs and events, notices, process jobs and output, model runs, and
individual provider rounds. A model run stores the requested and returned model
identity, connection name, non-secret settings, messages, text, thinking,
usage, retries, cost, timing, and failure details. Job signal requests are
durable rows too. Each message has a direct immutable compaction link when a
summary absorbs it. Later summaries link to their parent compaction and never
rewrite that older message link, so a client can rebuild the summary tree at any
time. Structured logs go to stdout and daily audit files. The audit log records
safe identifiers and digests. The transcript holds the verbatim data, so keep
its storage and every connected client as protected as the workspace itself.

## Process lifecycle

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for process and
service lifecycle plumbing. Peen owns the agent behavior, public API, storage,
and harness. Servicepack's framework details live in its own repository.

## External building blocks

- [Elelem](https://github.com/psyb0t/elelem) handles provider calls and tool
  rounds. [Essessey](https://github.com/psyb0t/essessey) turns its streamed
  content, tool calls, and thinking back into stable conversation blocks.
- [Aichteeteapee](https://github.com/psyb0t/aichteeteapee) runs the HTTP and
  WebSocket edge.
- [Servicepack](https://github.com/psyb0t/servicepack) owns process lifecycle.
- [Gonfiguration](https://github.com/psyb0t/gonfiguration),
  [ctxerrors](https://github.com/psyb0t/ctxerrors),
  [ctxscope](https://github.com/psyb0t/ctxscope),
  [slogging](https://github.com/psyb0t/slogging), and
  [goenv](https://github.com/psyb0t/goenv) cover configuration, errors,
  request scope, logging, and runtime environment detection.
- [GORM](https://github.com/go-gorm/gorm) persists the
  [SQLite](https://sqlite.org/) state, while
  [Prometheus' Go client](https://github.com/prometheus/client_golang) exposes
  application metrics.
