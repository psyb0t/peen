//go:build integration

package executionforms

import (
	"time"
)

const (
	executionFormsAPIMessagesPath    = "/api/v1/messages"
	executionFormsAPIOpenSessionPath = "/api/v1/sessions/open"
	executionFormsContentTypeJSON    = "application/json"
	executionFormsHeaderContentType  = "Content-Type"
	executionFormsWebSocketPath      = "/ws/v1"
	executionFormsReadyPath          = "/ready"
	executionFormsMetricsPath        = "/metrics"

	executionFormsHeaderAuthorization = "Authorization"
	executionFormsHeaderSessionID     = "X-Session-ID"

	executionFormsBearerPrefix = "Bearer "
	executionFormsAgentName    = "default"
	executionFormsAPIToken     = "EXAMPLE-DO-NOT-USE"

	// The refusal the socket rule prints when control is the default beside a
	// reachable Docker daemon.
	executionFormsDefaultSpaceSettingName = "PEEN_DEFAULT_AGENT_SPACE"
	executionFormsControlDefaultSetting   = "PEEN_DEFAULT_AGENT_SPACE=control"
	executionFormsHostRootMessage         = "host root"

	executionFormsDirectoryMode   = 0o700
	executionFormsFileMode        = 0o600
	executionFormsStartupTimeout  = 30 * time.Second
	executionFormsShutdownTimeout = 15 * time.Second
	executionFormsPollInterval    = 10 * time.Millisecond

	// executionFormsTurnTimeout bounds one complete turn, which is a longer
	// wait than startup: the controller launches a worker, the worker runs the
	// model, and only then does the completion event arrive. Reusing the
	// startup budget here made the suite flake under `make test-coverage`,
	// where the instrumented binary is slow enough that the read deadline
	// expired mid-turn. The client then closed the connection, cancelling the
	// controller's own runTurn call, which surfaced as a worker failure
	// rather than the client timeout it actually was.
	executionFormsTurnTimeout = 3 * time.Minute

	executionFormsAgentDocument = "---\nname: default\ndescription: execution form test agent\n---\nFollow the test instructions."
	executionFormsInitialRules  = "Initial workspace rule."
	executionFormsUpdatedRules  = "Updated workspace rule."

	executionFormsJSONFixtureFile  = "json-edit.txt"
	executionFormsPatchFixtureFile = "patch-edit.txt"
	executionFormsJSONCommandFile  = "json-command.txt"
	executionFormsPatchCommandFile = "patch-command.txt"

	executionFormsJSONBefore  = "before\n"
	executionFormsJSONAfter   = "after\n"
	executionFormsPatchBefore = "alpha\n"
	executionFormsPatchAfter  = "bravo\n"

	executionFormsJSONCommand  = "printf 'json command\\n' > json-command.txt"
	executionFormsPatchCommand = "printf 'patch command\\n' > patch-command.txt"
	executionFormsJSONOutput   = "json command\n"
	executionFormsPatchOutput  = "patch command\n"

	executionFormsJSONAnswer  = "json tool turn complete"
	executionFormsPatchAnswer = "patch tool turn complete"

	executionFormsSourceBinaryName    = "peen-source"
	executionFormsInstalledBinaryName = "cmd"
	executionFormsSourceForm          = "source build"
	executionFormsInstalledForm       = "go install"

	executionFormsRepositoryModuleFile = "go.mod"
	executionFormsCommandPackage       = "./cmd"
	executionFormsRunCommand           = "run"

	executionFormsConfigDirectory = "config"
	executionFormsStateDirectory  = "state"
	executionFormsSocketDirPrefix = "pw"
	executionFormsWorkspace       = "workspace"
	executionFormsAgentsDirectory = ".agents/agents"
	executionFormsAgentFile       = "default.md"
	executionFormsRulesFile       = "AGENTS.md"
	executionFormsDatabaseFile    = "peen.db"

	executionFormsRestartMessage = "confirm persisted history"
	executionFormsReloadMessage  = "confirm updated rules"
	executionFormsUnauthMessage  = "no token required"

	executionFormsUpstreamName               = "integration"
	executionFormsUpstreamProvider           = "openai"
	executionFormsUpstreamNameKey            = "name"
	executionFormsUpstreamTypeKey            = "type"
	executionFormsUpstreamURLKey             = "baseUrl"
	executionFormsMessageKey                 = "message"
	executionFormsWebSocketMetadataSessionID = "sessionId"
	executionFormsWebSocketProtocol          = "peen.v1"
	executionFormsWebSocketBearerPrefix      = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	executionFormsWebSocketMessageSend       = "message.send"
	executionFormsWebSocketCompleted         = "message.completed"
	executionFormsWebSocketFailed            = "message.failed"
	executionFormsMetricFamily               = "peen_http_requests_total"
	executionFormsNetworkTCP                 = "tcp"
	executionFormsLoopbackAddress            = "127.0.0.1:0"
	executionFormsJSONToolMessage            = "run the JSON tool turn"
	executionFormsPatchToolMessage           = "run the patch tool turn"
	executionFormsPatchText                  = "*** Begin Patch\n*** Update File: patch-edit.txt\n@@\n-alpha\n+bravo\n*** End Patch"
	executionFormsToolPathKey                = "path"
	executionFormsToolEditsKey               = "edits"
	executionFormsToolOldTextKey             = "old"
	executionFormsToolNewTextKey             = "new"
	executionFormsToolPatchKey               = "patch"
	executionFormsToolCommandKey             = "command"
	executionFormsToolPurposeKey             = "purpose"
	executionFormsJSONPurpose                = "write the JSON command marker"
	executionFormsPatchPurpose               = "write the patch command marker"

	executionFormsAgentEnvironmentPrefix = "PEEN_AGENT="
	executionFormsAgentUnsetMessage      = "confirm the default agent"
	executionFormsCoreGeneralPurpose     = "core:general-purpose"

	// executionFormsGeneralPurposeInstructions is the body of the embedded
	// general-purpose agent. A turn with PEEN_AGENT unset must send it to the
	// provider.
	executionFormsGeneralPurposeInstructions = "Carry out the user's request within the resolved instructions and tool"

	executionFormsRefusalTimeout  = 20 * time.Second
	executionFormsRefusalExitCode = 1

	executionFormsRetentionNotDayMessage = "PEEN_LOG_RETENTION must be a " +
		"positive whole number of days (a multiple of 24h), got 36h0m0s"
	executionFormsInvalidDisabledSkillsMessage = "invalid PEEN_DISABLED_CORE_SKILLS"
	executionFormsValidCoreSkillsMessage       = "valid names are delegation, freshness, plan-and-delegate, planning, writing-rules, writing-skills and all"
	executionFormsInvalidDisabledAgentsMessage = "invalid PEEN_DISABLED_CORE_AGENTS"
	executionFormsValidCoreAgentsMessage       = "valid names are general-purpose and all"
	executionFormsInvalidAgentMessage          = "invalid PEEN_AGENT"
	executionFormsDisabledCoreAgentMessage     = "is disabled by PEEN_DISABLED_CORE_AGENTS"
)
