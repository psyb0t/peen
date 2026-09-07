# Hook configuration

`hooks.yaml` adds ordered lifecycle actions to a Peen harness layer. Put the
base file at `PEEN_CONFIG_DIR/.agents/hooks.yaml`. Peen then finds another
`.agents/hooks.yaml` in each ancestor from the filesystem root through the
message workspace. It keeps every group in that order. A later layer adds
actions. It never replaces an earlier layer.

The config-directory layer is trusted deployment configuration and always
runs. Workspace hook files are still parsed, validated, and recorded in the
context snapshot, but their actions run only when
`PEEN_ENABLE_WORKSPACE_HOOKS=true`.

## File format

Every event names a list of groups. Each group has an optional `match` and a
nonempty, ordered `actions` list.

```yaml
version: 1
pre_write_file:
  - match:
      tool: write_file
      root: internal
      path: "**/*.go"
      extensions: [".go"]
      input:
        /expectedSha256:
          exists: true
    actions:
      - type: inject
        message: Read the Go rules before changing this file.
      - type: command
        command: scripts/check-go
        args: ["--workspace-check"]
        environment:
          CHECK_MODE: strict
        timeout_seconds: 20
      - type: emit_event
        event_type: hook.go.checked
        summary: Go write check completed.
        data:
          language: go
        delivery: queue
```

Unknown fields, unknown events, empty action lists, invalid regular
expressions, multiple YAML documents, and a version other than `1` reject the
turn before the hook can run.

## Events and ordering

Lifecycle events are `pre_user_message`, `post_user_message`, `session_start`,
`turn_start`, `turn_stop`, and `turn_cancelled`.

Every tool can use `pre_tool_use`, `post_tool_use`, and `tool_use_failure`.
File tools also have these specific events:

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

For a tool call, Peen runs `pre_tool_use` before its matching file event. A
successful file event runs before `post_tool_use`. A failed tool runs
`tool_use_failure`, then its matching file failure event, then
`post_tool_use`. Groups and actions always run serially.

## Matching

`match.tool` selects an exact tool name. File-aware events may also select
the canonical affected paths with these fields:

- `root`: a relative or absolute base directory. Paths outside it do not
  match.
- `path`: a slash-separated glob relative to `root`. `*`, `**`, and `?` are
  supported.
- `extensions`: one or more exact suffixes such as `.go` or `_test.go` is not
  an extension, so match it with `path`.
- `input`: a map from JSON Pointer to a predicate. A predicate may use
  `exists`, `equals`, or `regex`. All supplied predicates must match.

An action can also carry its own `when` matcher. Its match is evaluated after
the group match.

## Actions

`inject` has a required `message`. Peen adds it to the model at the next safe
boundary. User, session, and turn-start injections are appended to the system
prompt before the provider runs. Tool injections are user-role messages after
the tool result. A turn-stop or turn-cancelled injection has no later model
boundary and is not delivered.

`deny` has a required `reason` and stops the current operation. It never
degrades into a warning.

`emit_event` publishes a normal session event. It requires `event_type` and
`delivery` (`queue` or `wake`), and accepts `summary` and `data`. A new
session has no ID during `pre_user_message`, so an event action belongs in
`post_user_message` or a later event when it needs delivery.

`command` requires an executable `command` and accepts `args`, `environment`,
`working_dir`, and `timeout_seconds`. Peen starts the executable directly. It
does not invoke a shell. The child receives only `PATH` and the declared
environment, runs in the workspace unless `working_dir` changes it, and reads
one JSON invocation from standard input. The invocation contains the event,
session, request, turn, tool, call, workspace, affected paths, input, result,
and error when available.

A command may write this JSON object to standard output:

```json
{
  "decision": "allow",
  "message": "Additional model context.",
  "events": [
    {
      "type": "hook.checked",
      "summary": "The external check completed.",
      "data": {"ok": true},
      "delivery": "queue"
    }
  ]
}
```

Set `decision` to `deny` with a `reason` to reject the operation. Empty or
non-JSON output is allowed and means no extra effect.

## Failures and bounds

A failed action rejects a pre-event by default. A failed post or failure event
continues by default, records a warning, and publishes a bounded
`hook.action_failed` session event when a session exists. Set
`on_failure: deny` or `on_failure: continue` on an action to override that
default. The `deny` action always denies.

`PEEN_HOOK_COMMAND_TIMEOUT` and `PEEN_MAX_HOOK_COMMAND_OUTPUT` bound command
actions. A per-action `timeout_seconds` replaces the command timeout for that
action, but the whole tool call remains bounded by `PEEN_TOOL_TIMEOUT`.

Hooks run with the process user's filesystem and executable access. They are
not a sandbox or a permission system. Treat configuration-directory hooks as
deployment code. Enable workspace hooks only for workspaces you trust.
