# Naming

Every name Peen invents and owns is camelCase. Another convention is used only where something outside Peen owns the name.

## camelCase

- Tool names: `listFiles`, `searchText`, `readFile`, `writeFile`, `editFile`, `applyPatch`, `movePath`, `makeDirectory`, `removePath`, `runCommand`, `listJobs`, `readJobOutput`, `waitJob`, `signalJob`, `useSkill`, `launchAgent`.
- Hook event names: `preToolUse`, `postToolUse`, `toolUseFailure`, `pre<Tool>`, `post<Tool>` and `<tool>Failure` for each file tool (`preWriteFile`, `postWriteFile`, `writeFileFailure`), `preUserMessage`, `postUserMessage`, `sessionStart`, `turnStart`, `turnStop`, `turnCancelled`, `preCompact`, `postCompact`. The `event` field of hook stdin carries these names.
- `hooks.yaml` fields and action types: `workingDir`, `eventType`, `onFailure`, `emitEvent`, and any new multi-word key.
- Peen's own WebSocket frame and session event types. A dot separates namespace parts, and each part is camelCase: `userMessage.created`, `userMessage.queued`, `userMessage.delivered`, the event source `agent.launchAgent`, and every `agent.run.*` step.
- The worker protocol's frame kinds, commands, and error codes: `helloAck`, `commandResult`, `runTurn`, `signalJob`, `notFound`, `validationFailed`.
- JSON fields in the API, in hook stdin and stdout, and in session event data: `hookName`, `actionType`, `sourceEventId`.
- A new Peen-owned name follows the same rule in Go constants, YAML, JSON, TypeScript, and docs.

## Kept as they are

- `PEEN_*` environment variables, in `SCREAMING_SNAKE_CASE`. `LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE` belong to the logging library.
- SQL table and column names, in `snake_case`.
- Log record keys, in `lowercase_snake`. See `go-logging.md`.
- Prometheus metric names and label keys, in `snake_case`.
- The Anthropic Messages streaming frames carried over the WebSocket: `message_start`, `message_delta`, `message_stop`, `content_block_start`, `content_block_delta`, `content_block_stop`, `text_delta`, `thinking_delta`, `input_json_delta`, `tool_use`, `tool_result`, `tool_use_id`, `is_error`, `partial_json`, and the rest of that format.
- Skill and agent frontmatter keys fixed by the Agent Skills specification: `allowed-tools`, `user-invocable`, and the other specification keys.
- Embedded `SKILL.md` and agent file names, and the directory layout under `.agents/`.
- Names that a library, a protocol, or a vendored tool defines, such as the OpenAI chat completions fields and the Docker Engine API fields.
- Existing kebab-case values, such as `drop-oldest` for `PEEN_COMPACTION_MODE`, `ad-hoc` for an agent run definition, and the `event-handler` source kind. They are values, not names, and stay as they are. The camelCase rule is about not using `snake_case`.

## Enforcement

`internal/pkg/naming.IsValid` is the shape check. Tests walk every registered tool name (`internal/pkg/agent/tools_test.go`), every hook event and hook action type (`internal/pkg/harness/types_test.go`), and every Peen session event type (`internal/pkg/agent/types_test.go` and `internal/pkg/events/types_test.go`). They fail on an underscore or hyphen inside a dot-separated part, or a part that does not start with a lowercase letter. A new hook event goes into `harness.HookEvents` and a new event type into `agent.EventTypes`, which puts it under the test.
