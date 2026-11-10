//go:build real

package realtest

import (
	"time"
)

const (
	realHarnessTestTimeout = 20 * time.Minute

	// realHarnessIdleTimeout bounds the gap between two live events rather than
	// the whole turn. A real model doing a hundred tool rounds is slow but not
	// stuck, and an absolute deadline cannot tell those apart. Silence for this
	// long is the actual failure.
	realHarnessIdleTimeout     = 4 * time.Minute
	realHarnessStartupTimeout  = 30 * time.Second
	realHarnessShutdownTimeout = 15 * time.Second
	realHarnessPollInterval    = 50 * time.Millisecond
	realHarnessPageLimit       = 200

	realHarnessSocketDirectoryName = "pw"
	realHarnessWorkerSocketName    = "worker.sock"
	realHarnessMaxSocketPathBytes  = 107

	// realHarnessLifecycleTurns is the turn count before the wake event. One
	// user message is one turn: the child agent the task launches runs inside
	// it and must not open a turn of its own.
	realHarnessLifecycleTurns = 1

	// realHarnessMaxEvents bounds the durable event walk. It is a runaway
	// guard, not a limit on what a turn may emit.
	realHarnessMaxEvents      = 100000
	realHarnessDirectoryMode  = 0o700
	realHarnessFileMode       = 0o600
	realHarnessBinaryName     = "peen-real-harness"
	realHarnessCommandPackage = "./cmd"
	realHarnessRunCommand     = "run"
	realHarnessNetwork        = "tcp"
	realHarnessLoopback       = "127.0.0.1:0"
	realHarnessAPIPath        = "/api/v1"
	// realHarnessWebSocketPath is its own literal, not derived from
	// realHarnessAPIPath: the WebSocket and the REST API are separate
	// surfaces with separate paths.
	realHarnessWebSocketPath    = "/ws/v1"
	realHarnessMessagesPath     = realHarnessAPIPath + "/messages"
	realHarnessSessionPath      = realHarnessAPIPath + "/session"
	realHarnessEventsPath       = realHarnessSessionPath + "/events"
	realHarnessNoticesPath      = realHarnessSessionPath + "/notices"
	realHarnessWorkersPath      = realHarnessSessionPath + "/workers"
	realHarnessMessagesListPath = realHarnessSessionPath + "/messages"
	realHarnessAgentRunsPath    = realHarnessSessionPath + "/agents"
	realHarnessJobsPath         = realHarnessSessionPath + "/jobs"
	realHarnessTurnsPath        = realHarnessSessionPath + "/turns"
	realHarnessModelRunsPath    = realHarnessSessionPath + "/model-runs"

	realHarnessContextSnapshotsPath = realHarnessSessionPath + "/context-snapshots"
	realHarnessPromptSnapshotsPath  = realHarnessSessionPath + "/prompt-snapshots"
	realHarnessReadyPath            = "/ready"
	realHarnessAuthorization        = "Authorization"
	realHarnessSessionIDHeader      = "X-Session-ID"
	realHarnessContentType          = "Content-Type"
	realHarnessBearerPrefix         = "Bearer "
	realHarnessJSONMediaType        = "application/json"
	realHarnessWebSocketSessionID   = "sessionId"
	realHarnessWebSocketProtocol    = "peen.v1"
	realHarnessWebSocketBearer      = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	realHarnessMessageSendEvent     = "message.send"
	realHarnessMessageCompleted     = "message.completed"
	realHarnessMessageFailed        = "message.failed"
	realHarnessRootAgent            = "default"
	realHarnessSkill                = "fixture-service"
	realHarnessReviewer             = "fixture-reviewer"
	realHarnessPatchEvent           = "fixture.patch.applied"
	realHarnessWakeEvent            = "fixture.wake"
	realHarnessFixtureTestCommand   = "go test ./..."

	// realHarnessLongCommand is the only other command the fixture allows. A
	// job has to still be running for the signal endpoints to have anything to
	// act on, and the fixture's test command finishes too fast for that.
	realHarnessLongCommand = "sleep 300"

	realHarnessNotesFileName = "NOTES.md"

	// realHarnessNotesLineCount makes that file big enough that one read fills
	// a tool result at the deployment's result cap.
	realHarnessNotesLineCount    = 1500
	realHarnessPatchOnlyReason   = "Fixture changes must use applyPatch."
	realHarnessCommandOnlyReason = "Only the fixture test command is allowed."
	realHarnessHookHelperEnv     = "PEEN_REAL_HOOK_HELPER"
	realHarnessHookHelperEnabled = "enabled"
	realHarnessReadMarker        = "read-observed"
	realHarnessTaskMessage       = `This is an execution task with mandatory tool-call acceptance criteria, not a request for advice. Do not return a final answer until all criteria below have completed successfully. In this exact order: (1) call useSkill with fixture-service; (2) call launchAgent with fixture-reviewer, asking it to inspect internal/status/status.go and internal/status/status_test.go without changes; (3) after the child returns, call readFile yourself for each of those two paths; (4) call applyPatch, and only applyPatch, to make Label return "ready" and make the test expect "ready"; (5) call runCommand with exactly "go test ./..." from this workspace. Do not alter any other path.`

	realHarnessConfigRuleMarker    = "REAL_HARNESS_CONFIG_RULE_MARKER"
	realHarnessWorkspaceRuleMarker = "REAL_HARNESS_WORKSPACE_RULE_MARKER"
	realHarnessProjectsRuleMarker  = "REAL_HARNESS_PROJECTS_RULE_MARKER"
	realHarnessCatalogRuleMarker   = "REAL_HARNESS_CATALOG_RULE_MARKER"
	realHarnessServiceRuleMarker   = "REAL_HARNESS_SERVICE_RULE_MARKER"
	realHarnessSkillBodyMarker     = "REAL_HARNESS_SKILL_FULL_BODY_MARKER"
	realHarnessReviewerMarker      = "REAL_HARNESS_REVIEWER_FULL_BODY_MARKER"
	realHarnessWakeHandlerMarker   = "REAL_HARNESS_WAKE_HANDLER_FULL_BODY_MARKER"
	realHarnessRootAgentMarker     = "REAL_HARNESS_ROOT_AGENT_MARKER"

	realHarnessTrustedRuntimeMarker = "Trusted runtime context:"
	realHarnessOperatingSystemLead  = "Operating system: "
	realHarnessArchitectureLead     = "Architecture: "
	realHarnessTimezoneLead         = "Local timezone: "
)

// realHarnessBudgetSettings are the context and output settings the spawned
// controller never inherits from the deployment .env, so it runs each model
// with its full window and output limit.
func realHarnessBudgetSettings() []string {
	return []string{
		"PEEN_MAX_CONTEXT_TOKENS",
		"PEEN_OUTPUT_RESERVE_TOKENS",
		"PEEN_COMPACTION_MAX_OUTPUT_TOKENS",
		"PEEN_COMPACTION_SUMMARY_PERCENT",
	}
}
