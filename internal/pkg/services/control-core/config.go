package controlcore

import (
	"context"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

// runtimeOptions maps the deployment's configuration onto the runtime.
//
// The mapping itself lives in the agent package because a session worker builds
// its runtime from the same configuration. The control surface differs in one
// way: DefaultWorkspace stays unset, because a controller starts with no
// session rows and creates one only when a client opens a workspace through
// POST /v1/sessions/open.
func runtimeOptions(
	config peenconfig.Config,
	models agent.ModelResolver,
	metricRegistry *metrics.Metrics,
) agent.RuntimeOptions {
	return agent.RuntimeOptionsFromConfig(config, models, metricRegistry)
}

// harnessLimits is the single place the harness bounds are decided, so the
// resolver and anything derived from them cannot disagree.
func harnessLimits() harness.Limits {
	return agent.HarnessLimits()
}

// logValidatedConfig records the validated startup configuration shape with
// every secret redacted: never PEEN_API_TOKEN, and never a provider API key
// resolved through an upstream's apiKeyEnv. Only counts, names, bounds, and
// booleans are safe to print here.
//
//nolint:funlen // One record lists every safe config field.
func logValidatedConfig(
	ctx context.Context,
	config peenconfig.Config,
	upstreams []peenconfig.Upstream,
) {
	upstreamNames := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstreamNames = append(upstreamNames, upstream.Name)
	}

	ctxscope.GetLogger(ctx).Info(
		"validated configuration",
		"config_directory", config.ConfigDirectory,
		"state_directory", config.StateDirectory,
		"working_directory", config.WorkingDirectory,
		"agent", config.Agent,
		"default_model", config.DefaultModel,
		"compaction_model", config.CompactionModel,
		"compaction_mode", config.CompactionMode,
		"max_context_tokens", config.MaxContextTokens,
		"compaction_output_tokens", config.CompactionOutputTokens,
		"compaction_timeout", config.CompactionTimeout,
		"turn_timeout", config.TurnTimeout,
		"max_concurrent_turns", config.MaxConcurrentTurns,
		"max_queued_user_messages", config.MaxQueuedUserMessages,
		"max_message_bytes", config.MaxMessageBytes,
		"max_system_prompt_bytes", config.MaxSystemPromptBytes,
		"max_stored_message_bytes", config.MaxStoredMessageBytes,
		"max_tool_rounds", config.MaxToolRounds,
		"max_concurrent_tools", config.MaxConcurrentTools,
		"tool_timeout", config.ToolTimeout,
		"max_tool_result_tokens", config.MaxToolResultTokens,
		"enable_workspace_hooks", config.EnableWorkspaceHooks,
		"hook_command_timeout", config.HookCommandTimeout,
		"max_hook_command_output", config.MaxHookCommandOutput,
		"tool_max_list_entries", config.ToolMaxListEntries,
		"tool_max_list_depth", config.ToolMaxListDepth,
		"tool_max_search_matches", config.ToolMaxSearchMatches,
		"tool_max_search_file_bytes", config.ToolMaxSearchFileBytes,
		"tool_max_read_bytes", config.ToolMaxReadBytes,
		"tool_max_read_lines", config.ToolMaxReadLines,
		"tool_max_write_bytes", config.ToolMaxWriteBytes,
		"tool_max_edits", config.ToolMaxEdits,
		"tool_max_diff_bytes", config.ToolMaxDiffBytes,
		"tool_max_remove_entries", config.ToolMaxRemoveEntries,
		"tool_max_command_output_bytes", config.ToolMaxCommandOutputBytes,
		"tool_command_timeout", config.ToolCommandTimeout,
		"tool_max_command_timeout", config.ToolMaxCommandTimeout,
		"max_pending_events", config.MaxPendingEvents,
		"max_event_summary_bytes", config.MaxEventSummaryBytes,
		"max_event_data_bytes", config.MaxEventDataBytes,
		"max_event_wakes_per_hour", config.MaxEventWakesPerHour,
		"max_child_agent_depth", config.MaxChildAgentDepth,
		"max_child_agent_turns", config.MaxChildAgentTurns,
		"max_concurrent_agent_runs", config.MaxConcurrentAgentRunsPerSession,
		"max_agent_run_event_count", config.MaxAgentRunEventCount,
		"max_agent_run_event_bytes", config.MaxAgentRunEventBytes,
		"max_adhoc_agent_instruction_bytes",
		config.MaxAdHocAgentInstructionBytes,
		"http_listen_address", config.HTTPListenAddress,
		"metrics_listen_address", config.MetricsListenAddress,
		"worker_socket_root", config.WorkerSocketRoot(),
		"docker_authority", config.HasDockerAuthority(),
		"api_token_configured", config.APIToken != "",
		"provider_count", len(upstreams),
		"provider_names", upstreamNames,
	)
}
