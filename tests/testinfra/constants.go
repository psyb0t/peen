package testinfra

import (
	"time"
)

const (
	appDockerfile  = "Dockerfile"
	appBootTimeout = 25 * time.Minute

	// The image build pulls base images and packages, so it retries a
	// network blip: up to five attempts, waiting this long before the
	// second and doubling after each failure, well inside appBootTimeout.
	appImageBuildAttempts = 5
	appImageBuildBackoff  = 15 * time.Second

	appCleanupTimeout = 30 * time.Second
	// The configuration and state directories live outside the workspace
	// root, /tmp: Peen refuses a workspace that contains either of them, and
	// the tests open /tmp itself. /var/tmp is writable by any account, like
	// /tmp, which the coverage variant's host UID needs.
	appConfigDirectory = "/var/tmp/peen"
	// appStateDirectory is a sibling of appConfigDirectory, never a child.
	// Peen refuses to start when one contains the other, because the
	// read-only configuration mount would carry SQLite into every worker.
	appStateDirectory   = "/var/tmp/peen-state"
	stateFileCopyMode   = 0o600
	appWorkingDirectory = "/tmp"
	appAgentName        = "default"
	// The crash recovery suites prove that work continues in the next worker
	// or controller. Unit tests cover the paused default.
	appCrashRecovery           = "resume"
	appFixtureInstructionsPath = "/tmp/peen-test-AGENTS.md"
	appFixtureAgentPath        = "/tmp/peen-test-default.md"
	appFixtureFileMode         = 0o644
	appFixtureDirectoryMode    = 0o755
	// ConfigInstructions is the AGENTS.md the app container's config
	// directory holds.
	ConfigInstructions = "Follow the request and use available tools."
	//nolint:lll // Fixture content is byte exact and has no trailing newline.
	appAgentDocument    = "---\nname: default\ndescription: API integration test agent\n---\nFollow the request and return the result."
	appBootstrapCommand = `mkdir -p /var/tmp/peen/.agents/agents
mkdir -p /var/tmp/peen-state
cp /tmp/peen-test-AGENTS.md /var/tmp/peen/AGENTS.md
cp /tmp/peen-test-default.md /var/tmp/peen/.agents/agents/default.md
exec /app/app run`
	appNetwork                = "tcp"
	appHostNetwork            = "host"
	appLoopbackAddress        = "127.0.0.1:0"
	appURLPrefix              = "http://"
	appReadyPath              = "/ready"
	appRequestTimeout         = 30 * time.Second
	appRestartTimeout         = 30 * time.Second
	appCrashSignal            = "SIGKILL"
	appRestartPollInterval    = 10 * time.Millisecond
	appCoverageDirectory      = "/tmp/peen-coverage"
	appCoverageBuildArgument  = "PEEN_ENABLE_COVERAGE"
	appCoverageEnabled        = "true"
	appCoverageEnvironment    = "GOCOVERDIR"
	coverageDirectoryEnv      = "SERVICEPACK_COVDATA_DIR"
	providerName              = "integration"
	providerType              = "openai"
	providerModel             = "test-model"
	providerModelReference    = providerName + "/" + providerModel
	providerModelsPath        = "/models"
	providerModelsPathV1      = "/v1/models"
	providerCompletionsPath   = "/chat/completions"
	providerCompletionsPathV1 = "/v1/chat/completions"
	providerObjectField       = "object"
	providerModelsObjectType  = "list"
	providerModelObjectType   = "model"
	providerChunkObjectType   = "chat.completion.chunk"
	providerCompletionID      = "chatcmpl-integration"
	providerResponsePrefix    = "integration completion "
	providerCreatedAt         = 1
	jsonMediaType             = "application/json"
	eventStreamMediaType      = "text/event-stream"
	openAIDataPrefix          = "data: "
	openAIDoneEvent           = "data: [DONE]\n\n"
	// TestAPIToken is the deliberately fake bearer token injected into the
	// production container for API integration tests.
	TestAPIToken = "EXAMPLE-DO-NOT-USE"

	// TestProviderKey is the deliberately fake provider credential the
	// controller holds in the variable named by the test provider's
	// apiKeyEnv. A command the agent runs must never see it.
	TestProviderKey = "EXAMPLE-PROVIDER-KEY-DO-NOT-USE"

	// ProviderKeyEnvName is the variable that holds TestProviderKey.
	ProviderKeyEnvName = "PEEN_TEST_PROVIDER_KEY"

	// providerRoleSystem, providerRoleUser, providerRoleAssistant, and
	// providerRoleTool are the transcript roles the OpenAI wire format uses
	// for fixture messages. A scripted turn counts tool results to select its
	// current round.
	providerRoleSystem    = "system"
	providerRoleUser      = "user"
	providerRoleAssistant = "assistant"
	providerRoleTool      = "tool"

	// These names match internal/pkg/agent/tools.go's registrations.
	scriptedToolNameReadFile   = "readFile"
	scriptedToolNameEditFile   = "editFile"
	scriptedToolNameApplyPatch = "applyPatch"
	scriptedToolNameRunCommand = "runCommand"

	// These fixed call IDs let tests pair durable tool messages and SSE blocks.
	ScriptedCallIDReadFile   = "call-scripted-read-file"
	ScriptedCallIDEditFile   = "call-scripted-edit-file"
	ScriptedCallIDApplyPatch = "call-scripted-apply-patch"
	ScriptedCallIDRunCommand = "call-scripted-run-command"

	// scriptedRoundReadFile, scriptedRoundEditFile, and
	// scriptedRoundRunCommand are the tool-message counts that select each
	// scripted step; any higher count answers with plain text.
	scriptedRoundReadFile   = 0
	scriptedRoundEditFile   = 1
	scriptedRoundRunCommand = 2

	openAIToolCallType          = "function"
	openAIDeltaFieldRole        = "role"
	openAIDeltaFieldToolCalls   = "tool_calls"
	openAIDeltaReasoningContent = "reasoning_content"
	openAIFinishReasonStop      = "stop"
	openAIFinishReasonToolCalls = "tool_calls"
	openAIFieldIndex            = "index"
	openAIFieldDelta            = "delta"
	openAIFieldFinishReason     = "finish_reason"

	// ContainerWorkingDirectory is the process working directory used by the
	// application container, so a test can seed and read back fixture files
	// under the immutable tool workspace without duplicating the path.
	ContainerWorkingDirectory = appWorkingDirectory

	// DefaultProviderReasoning is the visible reasoning emitted by ordinary
	// provider completions in API integration tests.
	DefaultProviderReasoning = "integration reasoning"
)
