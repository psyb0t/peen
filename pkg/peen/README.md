# Embedding Peen

`pkg/peen` runs the durable agent engine inside a Go process. A `Runtime` owns SQLite, jobs, child agents, event delivery and harness resolution. A `Session` addresses one conversation. The engine starts no HTTP listener or Servicepack services and reads no `PEEN_` environment variables.

## Construct and close

Build an Elelem client with your chosen provider, then supply it under a qualified model reference:

```go
runtime, err := peen.New(peen.Options{
    ConfigDirectory: "/srv/peen/config",
    StateDirectory:  "/srv/peen/state",
    Workspace:       "/srv/project",
    RootAgent:       "core:general-purpose",
    Models: map[string]peen.ModelClient{
        "provider/model": {Client: client, Model: model},
    },
    DefaultModel: "provider/model",
})
if err != nil {
    return err
}
```

The config directory must exist. SQLite and hook state live in StateDirectory; leaving it empty uses ConfigDirectory with a `-state` suffix. Equal, nested or symlink-overlapping config and state directories fail validation. Workspace selects the initial session; leaving it empty captures the working directory once.

Call `runtime.Close(ctx)` after the last operation. Close refuses new work, waits for active calls, stops child agents and jobs, and closes SQLite, subscriptions and the private audit sink. If the context expires, Close cancels admitted calls and leaves resources available for a later Close to finish cleanup. Repeated successful calls are harmless. Each concurrent caller observes its own deadline. A synchronous event sink must return before its turn can finish; call Close outside a sink.

`InMemoryStore: true` uses the production SQLite schema in memory. Hook state uses a private temporary directory removed on Close. Runtime does not close caller-owned model clients, command runners or filesystems.

## Sessions and messages

`runtime.OpenSession(ctx, peen.OpenSessionOptions{Workspace: path})` creates or resumes the session keyed by the canonical workspace. `runtime.ResumeSession(ctx, id)` requires an existing record. `runtime.Session(id)` creates a handle; the next operation validates its ID and existence. `runtime.ListSessions(ctx, peen.PageRequest{Limit: 50})` lists durable session metadata.

```go
session, err := runtime.OpenSession(ctx, peen.OpenSessionOptions{
    Workspace: "/srv/another-project",
})
if err != nil {
    return err
}
result, err := session.Send(ctx, peen.MessageRequest{
    Message:         "inspect the project",
    Model:           "provider/model",
    ReasoningEffort: "low",
}, nil)
```

Send accepts an optional model, reasoning effort, system prompt, UUID request ID and To child run ID. Settings apply to a new root turn. A message delivered to an active turn or child's inbox returns `Queued: true` and an empty answer. Queued input cannot override turn settings. Queued root input is not replayed automatically after a restart; interrupted children restore accepted inbox messages through [child recovery](../../docs/agents.md#recovery-after-a-crash).

SystemPrompt uses PromptModeAppend or PromptModeReplace for one message. BaseSystemPrompt and AppendSystemPrompt configure the runtime; absent values retain the deployment prompt files and embedded default. An explicit AppendSystemPrompt replaces file-based append text while preserving the selected base. CompactionPrompt configures the summarizer.

A sink passed to Send receives ordered events for that turn. Returning an error aborts the turn. Legacy Runtime.Message, Stream, ListMessages, Details and Cancel address the initial session.

## Replay and control

All record types belong to `pkg/peen`. Reads return bounded pages with HasMore; increment Offset by the returned page size until it is false. Zero limit takes the default. Negative limits, offsets and cursors fail validation.

| Surface | Methods |
| --- | --- |
| Session state | Details, Cancel, Messages, Turns, Events |
| Compaction and prompts | Compactions, Compaction, ContextSnapshot, PromptSnapshot |
| Provider work | ModelRuns, ModelRunCalls |
| Notices | PublishEvent, Notices |
| `session.Jobs()` | List, Output, Signals, Signal |
| `session.Agents()` | List, Get, Events, Messages, Compactions, Compaction, Cancel, Send |

Job output and child events use sequence cursors; continue from NextCursor. Job signals accept `stop` or `kill`. Agent filters include `waiting`, `interrupted` and a direct parent run ID or `root`. Every record lookup checks the owning session. Cancel stops the active root turn and running children; jobs remain independent until explicitly signalled or the runtime closes.

Filters and controls accept the owned constants directly, including JobStateExited, JobStreamStdout, JobSignalStop, AgentRunStateCompleted and DeliveryQueue. AgentRunStateWaiting selects waiting children; their stored state remains AgentRunStateRunning.

PublishEvent validates the external event type, delivery mode, summary and data bounds. External events cannot impersonate reserved job or agent notices. Model cost amounts are decimal strings; optional timestamps and IDs remain optional in replay records.

## Live subscriptions

`session.Subscribe(ctx)` validates the session and returns a Subscription whose Events channel receives subsequent root, wake and child events, including children that outlive their launching turn. Each event carries its session and request IDs. Subscriptions are independent of a Send sink.

Call `subscription.Close()` when finished. Slow readers lose events once the configured buffer fills; Dropped counts the loss. Recover persisted records through Events and the child replay methods. Runtime.Close closes all subscription channels without waiting for readers. Cancelling the context used to create a subscription does not close it.

The root's ask tool publishes `question.asked`. Read it through Subscribe and answer with a later Send. Child questions appear in agent records and events; use To or Agents.Send to answer the child. See [agent messaging](../../docs/agents.md).

## Extension points

Options.Tools supplies custom Go handlers with a name, description and self-contained JSON object schema. Peen validates the schema at construction and each call's arguments before the handler runs. A custom tool with a built-in name replaces it. Hooks, tool timeouts, metrics, result bounds and agent allowed-tools apply to custom tools too. Messaging names `sendMessage`, `waitMessages`, `stopAgent` and `ask` remain reserved and always available. DisableTools removes selected workspace tools.

Options.Hooks maps a lifecycle event to a Go callback. File actions execute first, then the callback. Return HookOutcome with a denial, injected messages or external notices. A callback error denies the operation. Inputs contain the same lifecycle IDs, paths and JSON payloads as file hooks. See [hook events and actions](../../docs/hooks.md).

Options.ModelResolver resolves qualified model references at turn time. It replaces the static map and must return a non-nil client and a model ID. Default and compaction references are validated during construction.

Options.Harness supplies immutable maps of relative filenames to content. Each layer may contain AGENTS.md, `.agents/rules/`, skills, agents, event handlers and hooks.yaml; later layers override earlier names. `runtime.Harness(ctx, workspace)` returns the resolved instructions, catalogue, hooks, manifest and warnings. Memory layers follow filesystem discovery. Runtime turns and `changeDirectory` resolve only the current directory and its ancestors. `runtime.Harness` keeps its explicit inspection behavior, including scoped descendant AGENTS.md files. See [directory changes](../../docs/harness.md#when-it-is-read). Skill content is retained in the resolved snapshot; sibling resources must also be available through the workspace filesystem if a tool needs them.

Options.CommandRunner supplies a `commander.Commander` used for commands, supervised jobs and executable hook actions. The implementation owns execution isolation, honours cancellation for hook commands, and closes its output streams after completion. Hook output is collected with bounded buffers. Jobs retain the normal lifecycle, durable output and signal handling.

## Filesystems

| Adapter | Behavior |
| --- | --- |
| `peen.OSFS(directory)` | Full host filesystem access, with directory as the default workspace. Ancestor harness layers apply. |
| `peen.RootFS(directory)` | os.Root confines reads and atomic mutations to the directory. Absolute paths and root escapes fail. The caller closes the adapter after the runtime. |
| `peen.FromIOFS(files)` | Read-only adapter for io/fs.FS. The underlying filesystem defines its own containment. os.DirFS alone does not prevent symlink escapes. |

Supply an adapter as Options.Filesystem. A virtual filesystem also requires a stable WorkspaceID for durable session identity. File tools and harness discovery use relative paths under its root. Configuration, SQLite, hook state and audit files stay in host directories. No ancestor above a virtual root enters the harness. Opening another logical workspace ID with the same runtime addresses the same supplied filesystem with an independent transcript.

Virtual workspace SYSTEM.md, APPEND_SYSTEM.md and COMPACTION.md are read through the adapter. Explicit prompt values take precedence. Process and job tools and executable hook actions are disabled for a virtual workspace unless CommandRunner is supplied. A custom runner is responsible for mapping relative paths to its own execution environment.

Custom writable adapters must implement atomic replacement and exclusive creation or rename, preserve file modes, return standard io/fs errors, and provide a stable RealPath for aliases. File tools retain their read-before-change ledger and stale-content hashes.

## Limits and telemetry

The [option mapping](../../docs/embedding-options.md) lists deployment equivalents and defaults. ToolLimits, AgentLimits, HarnessLimits and EventLimits expose the corresponding in-process bounds. Zero values use their runtime defaults. Individual options cover concurrency, queue sizes, turn and tool timeouts, stored messages, prompt size, compaction and wake rates. `AskTimeout: 0` has no independent timeout. MaxConcurrentTurns and MaxEventWakesPerHour accept a negative value to disable their bound.

DisabledCoreSkills and DisabledCoreAgents remove selected embedded definitions. Logger selects a runtime-local slog sink while preserving request and session scope. AuditLog enables a private daily JSON sink with a whole-day retention duration. MetricsRegisterer receives an owned Peen collector which Close unregisters. Separate runtimes need separate registries, or registerers with distinct constant labels, to avoid duplicate descriptors.

See the executable examples in `example_test.go` and [development checks](../../docs/development.md).
