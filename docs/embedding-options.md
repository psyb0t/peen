# Embedding options

These are the in-process equivalents of deployment settings. `pkg/peen` accepts Go values and does not read these environment variables. The [embedding guide](../pkg/peen/README.md) covers lifecycle and construction. Zero-valued tuning fields take the defaults below; directories, models and the root agent require explicit construction choices.

| Deployment variable | Options field | Embedded default |
| --- | --- | --- |
| `PEEN_CONFIG_DIR` | `ConfigDirectory` | `caller-supplied` |
| `PEEN_STATE_DIR` | `StateDirectory` | `ConfigDirectory + "-state"` |
| `PEEN_AGENT` | `RootAgent` | `required explicitly` |
| `PEEN_DISABLED_CORE_SKILLS` | `DisabledCoreSkills` | empty |
| `PEEN_DISABLED_CORE_AGENTS` | `DisabledCoreAgents` | empty |
| `PEEN_UPSTREAMS` | `Models or ModelResolver` | `caller-supplied` |
| `PEEN_DEFAULT_MODEL` | `DefaultModel` | `caller-supplied` |
| `PEEN_COMPACTION_MODEL` | `CompactionModel` | `DefaultModel` |
| `PEEN_MAX_CONTEXT_TOKENS` | `MaxContextTokens` | `32768` |
| `PEEN_COMPACTION_MODE` | `CompactionMode` | `drop-oldest` |
| `PEEN_COMPACTION_MAX_OUTPUT_TOKENS` | `CompactionOutputTokens` | `2048` |
| `PEEN_COMPACTION_TIMEOUT` | `CompactionTimeout` | `2m` |
| `PEEN_TURN_TIMEOUT` | `TurnTimeout` | `10m` |
| `PEEN_MAX_CONCURRENT_TURNS` | `MaxConcurrentTurns` | `16` |
| `PEEN_MAX_QUEUED_USER_MESSAGES` | `MaxQueuedUserMessages` | `16` |
| `PEEN_MAX_MESSAGE_BYTES` | `MaxMessageBytes` | `262144` |
| `PEEN_MAX_SYSTEM_PROMPT_BYTES` | `MaxSystemPromptBytes` | `65536` |
| `PEEN_MAX_STORED_MESSAGE_BYTES` | `MaxStoredMessageBytes` | `1048576` |
| `PEEN_MAX_TOOL_ROUNDS` | `MaxToolRounds` | `32` |
| `PEEN_MAX_CONCURRENT_TOOLS` | `MaxConcurrentTools` | `4` |
| `PEEN_TOOL_TIMEOUT` | `ToolTimeout` | `15m` |
| `PEEN_MAX_TOOL_RESULT_TOKENS` | `MaxToolResultTokens` | `8192` |
| `PEEN_HOOK_COMMAND_TIMEOUT` | `HookCommandTimeout` | `30s` |
| `PEEN_MAX_HOOK_COMMAND_OUTPUT` | `MaxHookCommandOutput` | `65536` |
| `PEEN_TOOL_MAX_LIST_ENTRIES` | `ToolLimits.MaxListEntries` | `1000` |
| `PEEN_TOOL_MAX_LIST_DEPTH` | `ToolLimits.MaxListDepth` | `16` |
| `PEEN_TOOL_MAX_SEARCH_MATCHES` | `ToolLimits.MaxSearchMatches` | `200` |
| `PEEN_TOOL_MAX_SEARCH_FILE_BYTES` | `ToolLimits.MaxSearchFileBytes` | `2097152` |
| `PEEN_TOOL_MAX_READ_BYTES` | `ToolLimits.MaxReadBytes` | `262144` |
| `PEEN_TOOL_MAX_READ_LINES` | `ToolLimits.MaxReadLines` | `2000` |
| `PEEN_TOOL_MAX_WRITE_BYTES` | `ToolLimits.MaxWriteBytes` | `4194304` |
| `PEEN_TOOL_MAX_EDITS` | `ToolLimits.MaxEdits` | `64` |
| `PEEN_TOOL_MAX_DIFF_BYTES` | `ToolLimits.MaxDiffBytes` | `65536` |
| `PEEN_TOOL_MAX_REMOVE_ENTRIES` | `ToolLimits.MaxRemoveEntries` | `20000` |
| `PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES` | `ToolLimits.MaxCommandOutputBytes` | `65536` |
| `PEEN_TOOL_COMMAND_TIMEOUT` | `ToolLimits.CommandTimeout` | `2m` |
| `PEEN_TOOL_MAX_COMMAND_TIMEOUT` | `ToolLimits.MaxCommandTimeout` | `15m` |
| `PEEN_MAX_PENDING_EVENTS` | `EventLimits.MaxPendingPerSession` | `256` |
| `PEEN_MAX_EVENT_SUMMARY_BYTES` | `EventLimits.MaxSummaryBytes` | `4096` |
| `PEEN_MAX_EVENT_DATA_BYTES` | `EventLimits.MaxDataBytes` | `65536` |
| `PEEN_MAX_EVENT_WAKES_PER_HOUR` | `MaxEventWakesPerHour` | `60` |
| `PEEN_MAX_CHILD_AGENT_DEPTH` | `AgentLimits.MaxDepth` | `5` |
| `PEEN_MAX_CHILD_AGENT_TURNS` | `AgentLimits.MaxChildTurns` | `16` |
| `PEEN_MAX_CONCURRENT_AGENT_RUNS` | `AgentLimits.MaxConcurrentRuns` | `4` |
| `PEEN_MAX_AGENT_RUN_EVENT_COUNT` | `AgentLimits.MaxEventCount` | `2000` |
| `PEEN_MAX_AGENT_RUN_EVENT_BYTES` | `AgentLimits.MaxEventBytes` | `65536` |
| `PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES` | `AgentLimits.MaxAdHocInstructionBytes` | `65536` |
| `PEEN_ASK_TIMEOUT` | `AskTimeout` | `0` |

Audit settings map `PEEN_LOG_DIRECTORY` to `AuditLog.Directory` and `PEEN_LOG_RETENTION` to `AuditLog.Retention`, which defaults to 336 hours. Set `AuditLog.Enabled` to enable the private sink. An empty audit directory uses StateDirectory/logs. `Logger` supplies the slog sink; `MetricsRegisterer` supplies the metrics destination. The embedding package starts no metrics listener.

HarnessLimits uses the harness defaults: 256 files, 64 instructions, 64 skills, 64 agents, 64 event handlers, 256 hooks, 1024 directory entries, 128 KiB per file and 1 MiB total context. EventLimits.SubscriberBuffer defaults to 32. These values have no corresponding PEEN environment variables.

Additional ToolLimits fields cover patches and jobs: MaxPatchBytes 1 MiB, MaxPatchFiles 64, MaxPatchHunks 256, MaxPatchChangedBytes 4 MiB, MaxJobOutputLines 2000, MaxReadJobOutputLines 500, WaitJobTimeout 30 seconds, MaxWaitJobTimeout 10 minutes, JobStopGracePeriod 10 seconds and MaxListJobs 500. They use the same tool defaults as a controller.

Workspace defaults to the current directory captured at construction. Filesystem and WorkspaceID supply a virtual workspace instead. InMemoryStore uses ephemeral SQLite and temporary hook state. BaseSystemPrompt and AppendSystemPrompt provide prompt values; CompactionPrompt provides summarizer instructions. Harness, Tools, DisableTools, Hooks and CommandRunner provide extensions.

HTTP listeners and authentication, Docker profiles, worker supervision and controller workspace policy are deployment features. They have no in-process Options equivalent.
