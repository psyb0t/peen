# Hooks

A hook runs at a fixed point in a turn and does something mechanical: block a tool call, run a command, add context for the model, or publish a [session event](events.md). Use a hook for anything the model must not be able to skip. A [rule](rules.md) asks the model to do something. A hook makes it happen whether the model cooperates or not.

## Where hooks go

Hooks live in `.agents/hooks.yaml` in any [harness layer](harness.md#where-peen-looks). Put trusted, deployment-wide hooks in `PEEN_CONFIG_DIR/.agents/hooks.yaml` and project hooks in `<workspace>/.agents/hooks.yaml`. Every layer's groups are kept and run in layer order. A later layer adds to earlier ones and never replaces them.

Every layer's hooks run, the workspace's included. There is no switch. A workspace that should run no hooks has no `.agents/hooks.yaml`. A hook command runs with Peen's own access, so only open workspaces whose hooks you trust.

Peen re-reads `hooks.yaml` at the start of every turn, so an edit applies to the next message.

## File format

```yaml
version: 1
pre_tool_use:
  - name: no-force-push
    match:
      tool: run_command
      input:
        /command:
          regex: "git push .*--force"
    actions:
      - name: block
        type: deny
        reason: Never force-push. Push a new commit instead.
```

- `version` is required and must be `1`.
- Every other top-level key is a [hook event](#events), and its value is a list of groups.
- A group has an optional `name`, an optional `match`, and a non-empty, ordered `actions` list.
- An action has an optional `name`, a `type`, an optional `when` matcher, an optional `on_failure`, and the fields its type needs.

Give groups and actions a `name`. It shows up in logs and in `hook.action.failed` events. A missing group name becomes `<event>-<position>` and a missing action name becomes `<type>-<position>`, where positions count from 1, such as `pre_tool_use-1` or `deny-2`.

The YAML is strict. Unknown fields, unknown events, empty action lists, invalid regular expressions, an `emit_event` action whose `event_type` starts with `job.` or `agent.`, a second YAML document, or a version other than `1` make the whole file invalid. Peen then skips that file, keeps valid hooks from other layers, and reports a `harness.warning` that names the file and the reason.

Every action type shares one set of field names, so a field that belongs to another action type is not an unknown field. Peen accepts it and ignores it. A `type: deny` action with a `message` is valid, and the message does nothing.

## Events

A hook event is a fixed point in a turn where hooks run, such as `pre_write_file`. It is not a [session event](events.md), which is a record Peen hands to the model. See [Terms](architecture.md#terms).

### Lifecycle events

| Event | When it runs | Input |
| --- | --- | --- |
| `pre_user_message` | While Peen builds the prompt for a new turn, before the turn exists. | `{"message", "workspace", "model"}` |
| `session_start` | On the first turn a session ever runs, when the session has no earlier turns. | `{"created": true}` |
| `post_user_message` | After the turn is prepared. | `{"message", "workspace", "model"}` |
| `turn_start` | Right before `turn.started`. | `{"workspace", "model"}` |
| `turn_stop` | After a turn finishes successfully, before `turn.completed`. | `{"workspace", "model", "finishReason"}` |
| `turn_cancelled` | After the turn is cancelled. | `{"reason"}` |
| `pre_compact` | Before Peen summarizes old history. | `{"workspace", "estimatedTokens", "budgetTokens", "round", "unitsCovered", "messagesCovered"}` |
| `post_compact` | After the summary is stored. | Same as `pre_compact`, plus `summaryTokens` |

The input fields mean:

- `message`: the text of the user's message.
- `workspace`: the session's absolute workspace path.
- `model` on `pre_user_message` and `post_user_message`: the one-turn override the client sent in `data.model`. It is absent when the client sent none, which is the usual case.
- `model` on `turn_start` and `turn_stop`: the qualified model the turn runs, such as `zai/glm-5.3`. It is always present.
- `finishReason`: why the model's last response ended, as the provider reports it, such as `stop` or `length`.
- `reason`: always `cancelled`.
- `created`: always `true`.
- `estimatedTokens`: Peen's estimate of the request that went over the budget.
- `budgetTokens`: the token budget the request went over.
- `round`: the model round within the turn where compaction started.
- `unitsCovered` and `messagesCovered`: how many conversation units and stored messages the summary replaces.
- `summaryTokens`: the size of the stored summary in tokens.

A turn that fails or times out runs neither `turn_stop` nor `turn_cancelled`. A deny on `turn_stop` marks the turn failed even though the model already answered.

The compaction events only run when `PEEN_COMPACTION_MODE=summarize`.

An `emit_event` action needs a session. Every turn sent through the WebSocket has one, so it works on every hook event. If an action runs with no session, publishing fails. On `pre_` events, `session_start`, and `turn_start` that failure stops the turn unless the action sets `on_failure: continue`.

### Tool events

Every tool has `pre_tool_use`, `post_tool_use`, and `tool_use_failure`. File tools also have their own events:

| Tool | Before | Success | Failure |
| --- | --- | --- | --- |
| `read_file` | `pre_read_file` | `post_read_file` | `read_file_failure` |
| `list_files` | `pre_list_files` | `post_list_files` | `list_files_failure` |
| `search_text` | `pre_search_text` | `post_search_text` | `search_text_failure` |
| `write_file` | `pre_write_file` | `post_write_file` | `write_file_failure` |
| `edit_file` | `pre_edit_file` | `post_edit_file` | `edit_file_failure` |
| `apply_patch` | `pre_apply_patch` | `post_apply_patch` | `apply_patch_failure` |
| `move_path` | `pre_move_path` | `post_move_path` | `move_path_failure` |
| `remove_path` | `pre_remove_path` | `post_remove_path` | `remove_path_failure` |
| `make_directory` | `pre_make_directory` | `post_make_directory` | `make_directory_failure` |

`run_command`, the job tools, `use_skill`, and `launch_agent` have no events of their own. Use `pre_tool_use` with `match.tool` for them.

For one tool call the order is:

- before: `pre_tool_use`, then the tool's `pre_` event;
- on success: the tool's `post_` event, then `post_tool_use`;
- on failure: `tool_use_failure`, then the tool's `_failure` event, then `post_tool_use`.

The input of a tool event is the tool's own arguments, for example `{"path": "main.go", "content": "..."}` for `write_file` or `{"command": "make test", "purpose": "run tests"}` for `run_command`.

## Matching

A group's `match`, and an action's `when`, decide whether it runs. Leave both out to run on every occurrence of the event. Every field you set must match.

| Field | Matches |
| --- | --- |
| `tool` | The exact tool name. |
| `root` | Affected paths inside this directory, relative to the workspace or absolute. Without `root`, the workspace is the base. |
| `path` | Affected paths matching this glob, relative to `root`, or to the workspace when `root` is unset. `*`, `**`, and `?` work. |
| `extensions` | Affected paths whose extension is in the list. Each entry starts with `.`, such as `.go`. `_test.go` is not an extension, so match it with `path`. |
| `input` | A map from a JSON Pointer into the event input to a check: `exists: true`, `equals: <value>`, or `regex: <pattern>`. |

The affected paths come from the tool's arguments: `path` for most file tools, `source` and `destination` for `move_path`, every file a patch touches for `apply_patch`, and `directory` for `run_command`. When `root`, `path`, or `extensions` is set, at least one affected path must match. Lifecycle events have no paths, so a path matcher never matches them, but `input` works on their payload.

Only paths inside the base can match. Without `root`, a write to `/tmp/x.go` or any other path outside the workspace never matches `path` or `extensions`. Set `root: /` to match anywhere. `run_command` has an affected path only when the call sets `directory`, which it usually does not, so a guard on commands should use `match.tool` with `input` instead of a path matcher.

`input` has a few edge cases:

- A JSON Pointer walks objects only. It cannot index into an array, so `/paths/0` never exists.
- `regex` runs against the value formatted as text. A string matches as written. A number, boolean, or object is formatted the way Go prints it, so match strings where you can.
- `equals: null` counts as no check, and a matcher with no check makes the whole file invalid. Use `exists: false` to match a missing field.

## Actions

Actions in a group run one at a time, in order.

### `deny`

```yaml
- name: no-vendor-edits
  type: deny
  reason: Files under vendor/ are generated. Change the dependency list and regenerate vendor/ instead.
```

Stops the operation. On a tool event the model gets `reason` as the tool result, exactly as written, so the reason can carry Markdown or code. `reason` is required. A `deny` always denies, whatever `on_failure` says.

### `inject`

```yaml
- name: test-reminder
  type: inject
  message: Run make test before you report this as done.
```

Gives the model extra context. Where it lands depends on the event:

- `pre_user_message`, `session_start`, `post_user_message`, `turn_start`: appended to the system prompt under "Configured hook context".
- tool events: a user-role message right after that tool's result.
- `turn_stop`, `turn_cancelled`, `pre_compact`, `post_compact`: dropped, because no model call follows.

### `emit_event`

```yaml
- name: report-change
  type: emit_event
  event_type: repo.file.changed
  summary: A Go file changed.
  data:
    language: go
  delivery: queue
```

`type: emit_event` makes this action publish a [session event](events.md). Its fields:

| Field | Required | Meaning |
| --- | --- | --- |
| `event_type` | yes | The name of the session event, chosen by you, such as `repo.file.changed`. It becomes the event's `type` once published, and it is the file name a handler matches: `.agents/events/repo.file.changed.md`. It has 1 to 8 lowercase dotted parts, each starting with a letter and holding only letters and digits, up to 128 characters. It must not start with `job.` or `agent.`, which are reserved for Peen. It is not the action's `type`, which is always `emit_event` here. |
| `summary` | no | One line the model reads, such as "A Go file changed." Empty is allowed. |
| `data` | no | A YAML mapping, delivered to the model as JSON. |
| `delivery` | no | `queue` or `wake`. It decides whether the event wakes an idle session only when the handler for this type sets no `delivery` of its own. See [waking an idle session](events.md#waking-an-idle-session). |

The published event's `source` is always `hooks`. A `summary` longer than `PEEN_MAX_EVENT_SUMMARY_BYTES` or `data` larger than `PEEN_MAX_EVENT_DATA_BYTES` fails the action, and the normal [failure rules](#failures) apply.

### `command`

```yaml
- name: gofmt-check
  type: command
  command: /usr/local/bin/check-gofmt
  args: ["--strict"]
  environment:
    MODE: ci
  working_dir: .
  timeout_seconds: 20
```

Runs an executable. `command` is required. `args`, `environment`, `working_dir` (relative to the workspace), and `timeout_seconds` are optional. See [the command protocol](#the-command-protocol).

`timeout_seconds` has no upper cap. A command on a tool event still stops with the tool call at `PEEN_TOOL_TIMEOUT`. A command on a lifecycle event is not inside a tool call, so only `PEEN_TURN_TIMEOUT` bounds it.

## The command protocol

Peen runs the executable directly, with no shell. The process gets only `PATH` plus your `environment` entries, and runs in the workspace unless `working_dir` says otherwise.

Standard input is one JSON object:

```json
{
  "event": "pre_write_file",
  "sessionId": "7c1e…",
  "requestId": "0b9a…",
  "turnId": "4d2f…",
  "agentRunId": "only set inside a child agent",
  "tool": "write_file",
  "callId": "call_01",
  "workspace": "/home/me/work/my-api",
  "paths": ["/home/me/work/my-api/hello/main.go"],
  "input": {"path": "hello/main.go", "content": "package main…"},
  "stateDirectory": "/var/lib/peen/state/…/hook-state/7c1e…",
  "contextTokens": 18234
}
```

Fields that do not apply are left out. `sessionId`, `requestId`, and `turnId` are left out when there is no value, so `turnId` is absent on `pre_user_message`, which runs before the turn exists. `agentRunId`, `tool`, `callId`, `paths`, `input`, `result`, `error`, and `stateDirectory` are also left out when they do not apply. `result` and `error` appear on post and failure events. `stateDirectory` is a private directory for this session that keeps its contents between hook runs. It is only present when the session exists. Peen creates it under its writable state, never under `PEEN_CONFIG_DIR`. `contextTokens` is always present. It is Peen's estimate of the current context size, for local policy decisions, and may be `0`. The [identifiers](architecture.md#identifiers) section explains the ID fields.

Standard output may be empty, plain text, or this JSON object:

```json
{
  "decision": "deny",
  "reason": "Text the model gets instead of the tool result.",
  "message": "Extra context to inject.",
  "events": [
    {"type": "hook.checked", "summary": "Check ran.", "data": {"ok": true}, "delivery": "queue"}
  ]
}
```

- Empty output or output that is not JSON has no effect.
- JSON output is read strictly. It must be an object with only `decision`, `reason`, `message`, and `events`. Any other field fails the action. Output that parses as JSON but is not an object, such as a bare number or string, also fails the action.
- `decision` is `allow`, `deny`, or empty. `allow` and empty are the same. The action succeeds and the remaining actions still run. `allow` does not skip later checks, and no hook can override another hook's deny. A `deny` without a `reason` uses "hook command denied operation".
- `message` is injected the same way as an `inject` action. On `deny`, `message` is ignored.
- Each `events` entry has the same fields as an `emit_event` action, except that the event name is spelled `type` instead of `event_type`. Here `type` is the session event name, not an action kind. Its `data` must be a JSON object. An entry whose `type` starts with `job.` or `agent.`, or whose `summary` or `data` goes over the size limits, fails the action.
- Peen publishes the `events` before it applies the decision, so they are published even when the decision is `deny`.

The action fails when the command exits non-zero, runs past its timeout, or writes more than `PEEN_MAX_HOOK_COMMAND_OUTPUT` (64 KiB) to stdout or stderr.

## Failures

A `deny`, from a `deny` action or a command's decision, always stops the operation. What "the operation" is depends on the hook event:

| Hook event | What a deny does |
| --- | --- |
| A tool's `pre_` event, `pre_tool_use` | Refuses the tool call. The model gets `reason` as the tool result. |
| A tool's `post_` or `_failure` event, `post_tool_use`, `tool_use_failure` | Replaces the tool's result with an error. The tool has already run, so nothing is undone. |
| `pre_user_message`, `session_start`, `post_user_message`, `turn_start` | Fails the turn before the model is called. |
| `turn_stop` | Fails the turn, even though the model already answered. |
| `turn_cancelled` | Nothing more. The turn is already cancelled. |
| `pre_compact` | Stops the compaction, and the turn fails. |
| `post_compact` | Peen only logs it, because the summary is already stored. |

Any other failure follows `on_failure`. Without it:

- On every hook event whose name starts with `pre_` (including `pre_user_message` and `pre_compact`), and on `session_start` and `turn_start`, a failed action counts as a deny.
- On every other hook event, Peen logs a warning, publishes a `hook.action.failed` session event, and carries on.

Set `on_failure: deny` or `on_failure: continue` on an action to choose. A failure on a post event replaces the tool's result only when the action sets `on_failure: deny`.

A `hook.action.failed` event has the summary "Hook action failed after the operation." and this `data`:

| Key | Meaning |
| --- | --- |
| `event` | The hook event the action ran on, such as `post_write_file`. |
| `hook_name` | The group's name, or its `<event>-<position>` default. |
| `action` | The action's name, or its `<type>-<position>` default. |
| `action_type` | `deny`, `inject`, `emit_event`, or `command`. |
| `source` | The path of the `hooks.yaml` file that holds the action. This is not the event's own top-level `source`, which is `hooks`. |

These keys are snake_case, unlike the camelCase keys of other session events.

## Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEEN_HOOK_COMMAND_TIMEOUT` | `30s` | Time limit for one `command` action. `timeout_seconds` overrides it per action. The whole tool call stays bounded by `PEEN_TOOL_TIMEOUT`. |
| `PEEN_MAX_HOOK_COMMAND_OUTPUT` | `65536` | Maximum stdout or stderr from one command. |

At most 256 hook groups across all layers. Unlike an invalid file, which Peen skips, going over this limit fails every turn until you remove some groups. See [the harness limits](harness.md#mistakes-and-limits).

Hooks are not a sandbox. A hook command has the same filesystem and process access as Peen. Treat config-directory hooks as deployment code.

A workspace hook is a file the agent can edit or delete with its own file tools. A model that keeps getting denied may remove `.agents/hooks.yaml` to get past the block. A guard you rely on belongs in `PEEN_CONFIG_DIR`, with sessions on a [Docker execution profile](configuration.md#execution-profiles), which mounts the configuration directory read-only into the worker. On the `native` profile the agent has the same access to the configuration directory as Peen itself.

## Examples

### Block a dangerous command

```yaml
version: 1
pre_tool_use:
  - name: no-rm-rf
    match:
      tool: run_command
      input:
        /command:
          regex: "rm -rf /"
    actions:
      - name: block
        type: deny
        reason: Refusing to run rm -rf on the filesystem root.
```

### Format every Go file after a write

```yaml
version: 1
post_write_file:
  - name: gofmt
    match:
      extensions: [".go"]
    actions:
      - name: run-gofmt
        type: command
        command: /usr/local/go/bin/gofmt
        args: ["-l", "-w", "."]
        timeout_seconds: 20
```

### Remind the model on every turn

```yaml
version: 1
turn_start:
  - name: test-reminder
    actions:
      - name: remind
        type: inject
        message: Run make test before you say the work is done.
```

### Tell the session when a migration file changes

```yaml
version: 1
post_write_file:
  - name: migration-changed
    match:
      path: "db/migrations/*.sql"
    actions:
      - name: announce
        type: emit_event
        event_type: repo.migration.changed
        summary: A migration file changed. Run make migrate-check.
```

## Example: hand the model a rule once

Peen has no per-file rule loading. This recipe gives the model a project rule the first time it changes a matching file, then lets its retry through. It is one hooks file and two short scripts.

```yaml
version: 1
pre_write_file:
  - name: go-rules
    match:
      extensions: [".go"]
    actions:
      - name: deliver-go-rules
        type: command
        command: /opt/peen-hooks/deliver-rule.sh
        environment:
          RULE_FILE: /opt/peen-hooks/rules/go.md
          RULE_ID: go
pre_edit_file:
  - name: go-rules
    match:
      extensions: [".go"]
    actions:
      - name: deliver-go-rules
        type: command
        command: /opt/peen-hooks/deliver-rule.sh
        environment:
          RULE_FILE: /opt/peen-hooks/rules/go.md
          RULE_ID: go
post_compact:
  - name: forget-delivered-rules
    actions:
      - name: forget-rules
        type: command
        command: /opt/peen-hooks/forget-rules.sh
```

`deliver-rule.sh` keeps one marker per rule and per conversation in the hook's `stateDirectory`. A child agent has its own model context, so `agentRunId` keeps its marker apart from the root turn's:

```sh
#!/bin/sh
set -eu
invocation="$(cat)"
state="$(printf '%s' "$invocation" | jq -r '.stateDirectory')"
owner="$(printf '%s' "$invocation" | jq -r '.agentRunId // "root"')"
marker="$state/rule-$RULE_ID-$owner.delivered"
[ -f "$marker" ] && exit 0
: > "$marker"
exec jq -n --rawfile rule "$RULE_FILE" '{decision: "deny", reason: $rule}'
```

`forget-rules.sh` removes the markers after a compaction, because the summary may have dropped the rule from the model's context:

```sh
#!/bin/sh
set -eu
state="$(jq -r '.stateDirectory')"
find "$state" -maxdepth 1 -name 'rule-*.delivered' -delete
```

The first `.go` write is denied and the model reads the rule as the tool result. It writes again with the rule in mind, the marker exists, and the write goes through. The forget script clears every marker, so a compaction that lands between a denial and the model's retry delivers the rule once more. Keep the context budget comfortably above the size of your rule files so one write does not trigger a compaction on its own.

The scripts need `jq`. Keep each rule file within `PEEN_MAX_HOOK_COMMAND_OUTPUT`, because the rule travels as the command's output. A `post_compact` hook only runs in `summarize` compaction mode.

## Logging

At debug level Peen logs the start and finish of every hook event, including events with no matching group, and of every matching group and action, with `hook_name` and `hook_action_name`. The records include the outcome, duration, and safe identifiers. They never include hook input, tool content, command output, environment values, or credentials.
