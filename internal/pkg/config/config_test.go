package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testQualifiedModel  = "aigate/gateway-model"
	testDirectoryMode   = 0o700
	wantChildAgentDepth = 5
)

func TestConfigValidate(t *testing.T) {
	t.Setenv("PEEN_TEST_MISSING_KEY", "")
	validConfig := testConfig(t)

	testCases := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{
			name:   "valid keyless OpenAI gateway",
			mutate: func(*Config) {},
		},
		{
			name: "valid Z.ai Coding upstream",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[{"name":"zai","type":"zai-coding"}]`
			},
		},
		{
			name:    "relative config directory",
			mutate:  func(config *Config) { config.ConfigDirectory = "relative" },
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "relative state directory",
			mutate:  func(config *Config) { config.StateDirectory = "relative" },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "state directory inside the config directory",
			mutate: func(config *Config) {
				config.StateDirectory = filepath.Join(
					config.ConfigDirectory,
					"state",
				)
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "relative working directory",
			mutate:  func(config *Config) { config.WorkingDirectory = "relative" },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "metrics listener rejects public address",
			mutate: func(config *Config) {
				config.MetricsListenAddress = "0.0.0.0:9090"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "metrics listener requires a port",
			mutate: func(config *Config) {
				config.MetricsListenAddress = "127.0.0.1"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "missing default model",
			mutate:  func(config *Config) { config.DefaultModel = "" },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "disabled core skills and agents",
			mutate: func(config *Config) {
				config.DisabledCoreSkills = " planning , freshness "
				config.DisabledCoreAgents = "all"
				config.Agent = "reviewer"
			},
		},
		{
			name: "unknown disabled core skill",
			mutate: func(config *Config) {
				config.DisabledCoreSkills = "planning,bogus"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "unknown disabled core agent",
			mutate: func(config *Config) {
				config.DisabledCoreAgents = "default"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "root agent names an unknown core agent",
			mutate: func(config *Config) {
				config.Agent = "core:bogus"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "root agent names a disabled core agent",
			mutate: func(config *Config) {
				config.DisabledCoreAgents = "general-purpose"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "root agent names a core agent disabled by all",
			mutate: func(config *Config) {
				config.DisabledCoreAgents = "all"
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "unknown compaction mode",
			mutate:  func(config *Config) { config.CompactionMode = "unknown" },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "summary share and reply reserve fill the context",
			mutate: func(config *Config) {
				config.CompactionOutputTokens = config.MaxContextTokens
				config.CompactionSummaryPercent = MaxCompactionSummaryPercent
				config.OutputReserveTokens = config.MaxContextTokens / 2
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "summary share above half the context",
			mutate: func(config *Config) {
				config.CompactionSummaryPercent = MaxCompactionSummaryPercent + 1
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "negative summary share",
			mutate: func(config *Config) {
				config.CompactionSummaryPercent = -1
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "summary share of a context too small to hold one",
			mutate: func(config *Config) {
				config.MaxContextTokens = 2
				config.CompactionSummaryPercent = 1
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "half the context for the summary",
			mutate: func(config *Config) {
				config.CompactionOutputTokens = config.MaxContextTokens
				config.CompactionSummaryPercent = MaxCompactionSummaryPercent
			},
		},
		{
			name: "summary allowance and reply reserve fill the context",
			mutate: func(config *Config) {
				config.OutputReserveTokens = config.MaxContextTokens -
					config.CompactionOutputTokens
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "reply reserve fits beside the summary allowance",
			mutate: func(config *Config) {
				config.OutputReserveTokens = config.MaxContextTokens -
					config.CompactionOutputTokens - 1
			},
		},
		{
			name:    "negative reply reserve",
			mutate:  func(config *Config) { config.OutputReserveTokens = -1 },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "duplicate upstream name",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[
					{"name":"aigate","type":"openai"},
					{"name":"aigate","type":"anthropic"}
				]`
			},
			wantErr: ErrInvalidUpstream,
		},
		{
			name: "key environment is explicitly empty",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[
					{"name":"aigate","type":"openai","apiKeyEnv":"PEEN_TEST_MISSING_KEY"}
				]`
			},
			wantErr: ErrInvalidUpstream,
		},
		{
			name: "unsupported type",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[{"name":"aigate","type":"gemini"}]`
			},
			wantErr: ErrInvalidUpstream,
		},
		{
			// The v0.10.0 rename has no compatibility path, so an entry
			// still spelling the key "provider" must fail rather than
			// start with whatever the empty type selects. The unknown key
			// is dropped during decoding, which leaves the type empty.
			name: "the pre-rename provider key is refused",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[{"name":"aigate","provider":"openai"}]`
			},
			wantErr: ErrInvalidUpstream,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := validConfig
			tc.mutate(&config)

			err := config.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestParseUsesFixedEnvironmentBindings(t *testing.T) {
	configDirectory := filepath.Join(t.TempDir(), "config")
	workingDirectory := filepath.Join(t.TempDir(), "workspace")
	ignoredWorkingDirectory := filepath.Join(t.TempDir(), "ignored-workspace")
	require.NoError(t, os.Mkdir(workingDirectory, testDirectoryMode))
	t.Chdir(workingDirectory)
	t.Setenv("PEEN_CONFIG_DIR", configDirectory)
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_WORKING_DIR", ignoredWorkingDirectory)
	t.Setenv("PEEN_AGENT", "coding")
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_COMPACTION_MODEL", testQualifiedModel)
	t.Setenv("PEEN_MAX_CONTEXT_TOKENS", "8192")
	t.Setenv("PEEN_OUTPUT_RESERVE_TOKENS", "2048")
	t.Setenv("PEEN_COMPACTION_MODE", string(CompactionModeSummarize))
	t.Setenv("PEEN_COMPACTION_MAX_OUTPUT_TOKENS", "1024")
	t.Setenv("PEEN_COMPACTION_SUMMARY_PERCENT", "20")
	t.Setenv("PEEN_MODEL_REQUEST_TIMEOUT", "4m")
	t.Setenv("PEEN_DEFAULT_TOOL_COMMAND_TIMEOUT", "30m")
	t.Setenv("PEEN_HTTP_LISTEN_ADDRESS", "127.0.0.1:8081")
	t.Setenv("PEEN_METRICS_LISTEN_ADDRESS", "127.0.0.1:9091")
	t.Setenv("PEEN_API_TOKEN", "test-token")
	t.Setenv("PEEN_MAX_CONCURRENT_TURNS", "8")
	t.Setenv("PEEN_MAX_QUEUED_USER_MESSAGES", "7")
	t.Setenv("PEEN_MAX_MESSAGE_BYTES", "131072")
	t.Setenv("PEEN_MAX_SYSTEM_PROMPT_BYTES", "32768")
	t.Setenv("PEEN_MAX_STORED_MESSAGE_BYTES", "524288")
	t.Setenv("PEEN_MAX_IMAGES_PER_MESSAGE", "2")
	t.Setenv("PEEN_MAX_IMAGE_BYTES", "1048576")
	t.Setenv("PEEN_DEFAULT_HOOK_COMMAND_TIMEOUT", "20s")
	t.Setenv("PEEN_MAX_HOOK_COMMAND_OUTPUT", "8192")

	config, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, configDirectory, config.ConfigDirectory)
	assert.Equal(t, workingDirectory, config.WorkingDirectory)
	assert.Equal(t, "coding", config.Agent)
	assert.Equal(t, testQualifiedModel, config.DefaultModel)
	assert.Equal(t, CompactionModeSummarize, config.CompactionMode)
	assert.Equal(t, 8192, config.MaxContextTokens)
	assert.Equal(t, 2048, config.OutputReserveTokens)
	assert.Equal(t, 1024, config.CompactionOutputTokens)
	assert.Equal(t, 20, config.CompactionSummaryPercent)
	assert.Equal(t, 1024, config.CompactionSummaryTokens())
	assert.Equal(t, 4*time.Minute, config.ModelRequestTimeout)
	assert.Equal(t, 30*time.Minute, config.DefaultToolCommandTimeout)
	assert.Equal(t, "127.0.0.1:8081", config.HTTPListenAddress)
	assert.Equal(t, "127.0.0.1:9091", config.MetricsListenAddress)
	assert.Equal(t, 8, config.MaxConcurrentTurns)
	assert.Equal(t, 7, config.MaxQueuedUserMessages)
	assert.Equal(t, 131072, config.MaxMessageBytes)
	assert.Equal(t, 32768, config.MaxSystemPromptBytes)
	assert.Equal(t, 524288, config.MaxStoredMessageBytes)
	assert.Equal(t, 2, config.MaxImagesPerMessage)
	assert.Equal(t, 1048576, config.MaxImageBytes)
	assert.Equal(t, 20*time.Second, config.DefaultHookCommandTimeout)
	assert.Equal(t, 8192, config.MaxHookCommandOutput)

	upstreams, err := config.Upstreams()
	require.NoError(t, err)
	require.Len(t, upstreams, 1)
	assert.Empty(t, upstreams[0].APIKey())
}

func TestParseDefaultsWorkingDirectoryToProcessDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)

	config, err := Parse()

	require.NoError(t, err)
	assert.Equal(t, workingDirectory, config.WorkingDirectory)
	assert.Equal(t, wantChildAgentDepth, config.MaxChildAgentDepth)
}

func testConfig(t *testing.T) Config {
	t.Helper()

	return Config{
		ConfigDirectory:        filepath.Join(t.TempDir(), "config"),
		StateDirectory:         filepath.Join(t.TempDir(), "state"),
		WorkingDirectory:       filepath.Join(t.TempDir(), "workspace"),
		Agent:                  "core:general-purpose",
		UpstreamsJSON:          `[{"name":"aigate","type":"openai"}]`,
		DefaultModel:           testQualifiedModel,
		MaxContextTokens:       8192,
		CompactionMode:         CompactionModeDropOldest,
		CrashRecovery:          CrashRecoveryPause,
		CompactionOutputTokens: 1024,
		ModelRequestTimeout:    time.Minute,
		HTTPListenAddress:      ":8080",
		MetricsListenAddress:   "127.0.0.1:9090",

		// Mirrors the `default:` tags, which gonfiguration applies on Parse
		// and a struct literal does not get.
		MaxToolRounds:             32,
		MaxConcurrentTools:        4,
		ToolTimeout:               15 * time.Minute,
		MaxToolResultTokens:       8192,
		ToolMaxListEntries:        1000,
		ToolMaxListDepth:          16,
		ToolMaxSearchMatches:      200,
		ToolMaxSearchFileBytes:    2097152,
		ToolMaxReadBytes:          262144,
		ToolMaxReadLines:          2000,
		ToolMaxWriteBytes:         4194304,
		ToolMaxEdits:              64,
		ToolMaxDiffBytes:          65536,
		ToolMaxRemoveEntries:      20000,
		ToolMaxCommandOutputBytes: 65536,
		DefaultToolCommandTimeout: 10 * time.Minute,
		DefaultHookCommandTimeout: 30 * time.Second,
		MaxHookCommandOutput:      65536,
		MaxPendingEvents:          256,
		MaxEventSummaryBytes:      4096,
		MaxEventDataBytes:         65536,
		MaxEventWakesPerHour:      60,

		// No test reads the machine's DOCKER_HOST or its real Docker socket.
		dockerEnv: &dockerEnvironment{
			defaultSocket: filepath.Join(t.TempDir(), "no-docker.sock"),
		},
	}
}
