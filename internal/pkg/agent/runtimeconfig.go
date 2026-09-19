package agent

import (
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

// RuntimeOptionsFromConfig maps a deployment's configuration onto the runtime.
//
// Both the control plane and a session worker build a runtime from the same
// configuration, so this mapping lives here rather than in either of them. Two
// copies is how a setting gets wired in one place and forgotten in the other.
//
// Store and Resolver are left zero. The control plane fills them from its own
// SQLite handle, and a worker fills them with the protocol-backed store and its
// own resolver, because a worker never opens the controller's database.
func RuntimeOptionsFromConfig(
	deployment config.Config,
	models ModelResolver,
	metricRegistry *metrics.Metrics,
) RuntimeOptions {
	return RuntimeOptions{
		Models:       models,
		RootAgent:    deployment.Agent,
		DefaultModel: deployment.DefaultModel,

		MaxContextTokens: deployment.MaxContextTokens,
		TurnTimeout:      deployment.TurnTimeout,

		MaxSystemPromptBytes:  deployment.MaxSystemPromptBytes,
		MaxMessageBytes:       deployment.MaxMessageBytes,
		MaxConcurrentTurns:    deployment.MaxConcurrentTurns,
		MaxQueuedUserMessages: deployment.MaxQueuedUserMessages,

		CompactionMode:         deployment.CompactionMode,
		CompactionModel:        deployment.CompactionModel,
		CompactionOutputTokens: deployment.CompactionOutputTokens,
		CompactionTimeout:      deployment.CompactionTimeout,

		Events: events.NewBus(
			EventBusOptions(deployment, metricRegistry),
		),
		Metrics:              metricRegistry,
		MaxEventWakesPerHour: deployment.MaxEventWakesPerHour,
		ConfigDirectory:      deployment.ConfigDirectory,
		AgentLimits:          ConfiguredAgentRunLimits(deployment),
		ToolLimits:           ConfiguredToolLimits(deployment),
		MaxToolRounds:        deployment.MaxToolRounds,
		MaxConcurrentTools:   deployment.MaxConcurrentTools,
		ToolTimeout:          deployment.ToolTimeout,
		MaxToolResultTokens:  deployment.MaxToolResultTokens,
		EnableWorkspaceHooks: deployment.EnableWorkspaceHooks,
		HookCommandTimeout:   deployment.HookCommandTimeout,
		MaxHookCommandOutput: deployment.MaxHookCommandOutput,
	}
}

// EventBusOptions maps the deployment's event bounds onto the event package.
func EventBusOptions(
	deployment config.Config,
	metricRegistry *metrics.Metrics,
) events.Options {
	return events.Options{
		MaxPendingPerSession: deployment.MaxPendingEvents,
		MaxSummaryBytes:      deployment.MaxEventSummaryBytes,
		MaxDataBytes:         deployment.MaxEventDataBytes,
		Metrics:              metricRegistry,
	}
}

// ConfiguredAgentRunLimits maps the deployment's child-agent bounds onto the
// runtime. Without this the env vars parse and validate but never reach
// launch_agent, so an operator would set them and see nothing change.
func ConfiguredAgentRunLimits(deployment config.Config) AgentRunLimits {
	return AgentRunLimits{
		MaxDepth:                 deployment.MaxChildAgentDepth,
		MaxChildTurns:            deployment.MaxChildAgentTurns,
		MaxConcurrentRuns:        deployment.MaxConcurrentAgentRunsPerSession,
		MaxEventCount:            deployment.MaxAgentRunEventCount,
		MaxEventBytes:            deployment.MaxAgentRunEventBytes,
		MaxAdHocInstructionBytes: adHocInstructionBytes(deployment),
	}
}

// adHocInstructionBytes keeps an inline agent definition inside the bound a
// stored agent file already has.
//
// The two settings come from different places, the deployment's own env var
// and the harness file bound, so equal defaults are not enough: raising one
// alone would let an inline definition carry instructions the same deployment
// would refuse to read from disk. Taking the smaller of the two makes the
// contract hold no matter which one moves.
func adHocInstructionBytes(deployment config.Config) int {
	storedFileBytes := HarnessLimits().MaxFileBytes
	if int64(deployment.MaxAdHocAgentInstructionBytes) > storedFileBytes {
		return int(storedFileBytes)
	}

	return deployment.MaxAdHocAgentInstructionBytes
}

// HarnessLimits is the single place the harness bounds are decided, so the
// resolver and anything derived from them cannot disagree.
func HarnessLimits() harness.Limits {
	return harness.DefaultLimits()
}

// ConfiguredToolLimits maps the deployment's tool bounds onto the tool package.
// These are resource and context bounds, never permission gates.
func ConfiguredToolLimits(deployment config.Config) tools.Limits {
	return tools.Limits{
		MaxListEntries:        deployment.ToolMaxListEntries,
		MaxListDepth:          deployment.ToolMaxListDepth,
		MaxSearchMatches:      deployment.ToolMaxSearchMatches,
		MaxSearchFileBytes:    deployment.ToolMaxSearchFileBytes,
		MaxReadBytes:          deployment.ToolMaxReadBytes,
		MaxReadLines:          deployment.ToolMaxReadLines,
		MaxWriteBytes:         deployment.ToolMaxWriteBytes,
		MaxEdits:              deployment.ToolMaxEdits,
		MaxDiffBytes:          deployment.ToolMaxDiffBytes,
		MaxRemoveEntries:      deployment.ToolMaxRemoveEntries,
		MaxCommandOutputBytes: deployment.ToolMaxCommandOutputBytes,
		CommandTimeout:        deployment.ToolCommandTimeout,
		MaxCommandTimeout:     deployment.ToolMaxCommandTimeout,
	}
}
