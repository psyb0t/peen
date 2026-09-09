package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testQualifiedModel = "aigate/gateway-model"

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
				config.UpstreamsJSON = `[{"name":"zai","provider":"zai-coding"}]`
			},
		},
		{
			name:    "relative config directory",
			mutate:  func(config *Config) { config.ConfigDirectory = "relative" },
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
			name:    "unknown compaction mode",
			mutate:  func(config *Config) { config.CompactionMode = "unknown" },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "compaction output reaches context limit",
			mutate: func(config *Config) {
				config.CompactionOutputTokens = config.MaxContextTokens
			},
			wantErr: ErrInvalidConfig,
		},
		{
			name: "duplicate upstream name",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[
					{"name":"aigate","provider":"openai"},
					{"name":"aigate","provider":"anthropic"}
				]`
			},
			wantErr: ErrInvalidUpstream,
		},
		{
			name: "key environment is explicitly empty",
			mutate: func(config *Config) {
				config.UpstreamsJSON = `[
					{"name":"aigate","provider":"openai","apiKeyEnv":"PEEN_TEST_MISSING_KEY"}
				]`
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
	t.Setenv("PEEN_CONFIG_DIR", configDirectory)
	t.Setenv("PEEN_WORKING_DIR", workingDirectory)
	t.Setenv("PEEN_AGENT", "coding")
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","provider":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_COMPACTION_MODEL", testQualifiedModel)
	t.Setenv("PEEN_MAX_CONTEXT_TOKENS", "8192")
	t.Setenv("PEEN_COMPACTION_MODE", string(CompactionModeSummarize))
	t.Setenv("PEEN_COMPACTION_MAX_OUTPUT_TOKENS", "1024")
	t.Setenv("PEEN_COMPACTION_TIMEOUT", "3m")
	t.Setenv("PEEN_TURN_TIMEOUT", "4m")
	t.Setenv("PEEN_HTTP_LISTEN_ADDRESS", "127.0.0.1:8081")
	t.Setenv("PEEN_METRICS_LISTEN_ADDRESS", "127.0.0.1:9091")
	t.Setenv("PEEN_API_TOKEN", "test-token")
	t.Setenv("PEEN_MAX_CONCURRENT_TURNS", "8")
	t.Setenv("PEEN_MAX_QUEUED_USER_MESSAGES", "7")
	t.Setenv("PEEN_MAX_MESSAGE_BYTES", "131072")
	t.Setenv("PEEN_MAX_SYSTEM_PROMPT_BYTES", "32768")
	t.Setenv("PEEN_MAX_STORED_MESSAGE_BYTES", "524288")
	t.Setenv("PEEN_ENABLE_WORKSPACE_HOOKS", "true")
	t.Setenv("PEEN_HOOK_COMMAND_TIMEOUT", "20s")
	t.Setenv("PEEN_MAX_HOOK_COMMAND_OUTPUT", "8192")

	config, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, configDirectory, config.ConfigDirectory)
	assert.Equal(t, workingDirectory, config.WorkingDirectory)
	assert.Equal(t, "coding", config.Agent)
	assert.Equal(t, testQualifiedModel, config.DefaultModel)
	assert.Equal(t, CompactionModeSummarize, config.CompactionMode)
	assert.Equal(t, 8192, config.MaxContextTokens)
	assert.Equal(t, 1024, config.CompactionOutputTokens)
	assert.Equal(t, 3*time.Minute, config.CompactionTimeout)
	assert.Equal(t, 4*time.Minute, config.TurnTimeout)
	assert.Equal(t, "127.0.0.1:8081", config.HTTPListenAddress)
	assert.Equal(t, "127.0.0.1:9091", config.MetricsListenAddress)
	assert.Equal(t, 8, config.MaxConcurrentTurns)
	assert.Equal(t, 7, config.MaxQueuedUserMessages)
	assert.Equal(t, 131072, config.MaxMessageBytes)
	assert.Equal(t, 32768, config.MaxSystemPromptBytes)
	assert.Equal(t, 524288, config.MaxStoredMessageBytes)
	assert.True(t, config.EnableWorkspaceHooks)
	assert.Equal(t, 20*time.Second, config.HookCommandTimeout)
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
	t.Setenv("PEEN_WORKING_DIR", "")
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","provider":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)

	config, err := Parse()

	require.NoError(t, err)
	assert.Equal(t, workingDirectory, config.WorkingDirectory)
}

func testConfig(t *testing.T) Config {
	t.Helper()

	return Config{
		ConfigDirectory:        filepath.Join(t.TempDir(), "config"),
		WorkingDirectory:       filepath.Join(t.TempDir(), "workspace"),
		Agent:                  "default",
		UpstreamsJSON:          `[{"name":"aigate","provider":"openai"}]`,
		DefaultModel:           testQualifiedModel,
		MaxContextTokens:       8192,
		CompactionMode:         CompactionModeDropOldest,
		CompactionOutputTokens: 1024,
		CompactionTimeout:      time.Minute,
		TurnTimeout:            time.Minute,
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
		ToolCommandTimeout:        2 * time.Minute,
		ToolMaxCommandTimeout:     15 * time.Minute,
		HookCommandTimeout:        30 * time.Second,
		MaxHookCommandOutput:      65536,
		MaxPendingEvents:          256,
		MaxEventSummaryBytes:      4096,
		MaxEventDataBytes:         65536,
		MaxEventWakesPerHour:      60,
	}
}

// clearToolBounds models a caller who set no tool bound at all, which the
// `default:` tags would have filled during Parse.
func clearToolBounds(c *Config) {
	c.MaxToolRounds = 0
	c.MaxConcurrentTools = 0
	c.ToolTimeout = 0
	c.MaxToolResultTokens = 0
	c.ToolMaxListEntries = 0
	c.ToolMaxListDepth = 0
	c.ToolMaxSearchMatches = 0
	c.ToolMaxSearchFileBytes = 0
	c.ToolMaxReadBytes = 0
	c.ToolMaxReadLines = 0
	c.ToolMaxWriteBytes = 0
	c.ToolMaxEdits = 0
	c.ToolMaxDiffBytes = 0
	c.ToolMaxRemoveEntries = 0
	c.ToolMaxCommandOutputBytes = 0
	c.ToolCommandTimeout = 0
	c.ToolMaxCommandTimeout = 0
	c.HookCommandTimeout = 0
	c.MaxHookCommandOutput = 0
	c.MaxPendingEvents = 0
	c.MaxEventSummaryBytes = 0
	c.MaxEventDataBytes = 0
	c.MaxEventWakesPerHour = 0
}

func TestConfigValidateToolLimits(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:   "defaults are valid",
			mutate: func(*Config) {},
		},
		{
			name:   "zero means take the package default",
			mutate: func(c *Config) { c.MaxToolRounds = 0 },
		},
		{
			name:   "an entirely unset tool section is valid",
			mutate: clearToolBounds,
		},
		{
			name:    "negative tool round bound",
			mutate:  func(c *Config) { c.MaxToolRounds = -1 },
			wantErr: true,
		},
		{
			name:    "negative read bound",
			mutate:  func(c *Config) { c.ToolMaxReadBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative search file bound",
			mutate:  func(c *Config) { c.ToolMaxSearchFileBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative command timeout",
			mutate:  func(c *Config) { c.ToolCommandTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "negative hook command timeout",
			mutate:  func(c *Config) { c.HookCommandTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "negative hook command output limit",
			mutate:  func(c *Config) { c.MaxHookCommandOutput = -1 },
			wantErr: true,
		},
		{
			name: "hook command timeout outlives the harness tool timeout",
			mutate: func(c *Config) {
				c.ToolTimeout = time.Minute
				c.HookCommandTimeout = time.Hour
			},
			wantErr: true,
		},
		{
			name: "command maximum outlives the harness tool timeout",
			mutate: func(c *Config) {
				c.ToolTimeout = time.Minute
				c.ToolMaxCommandTimeout = time.Hour
			},
			wantErr: true,
		},
		{
			name: "default command timeout above the maximum",
			mutate: func(c *Config) {
				c.ToolCommandTimeout = time.Hour
				c.ToolMaxCommandTimeout = time.Minute
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := testConfig(t)
			tc.mutate(&config)

			err := config.Validate()
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestConfigValidateMessageLimits(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:   "defaults are valid",
			mutate: func(*Config) {},
		},
		{
			name: "zero means take the package default",
			mutate: func(c *Config) {
				c.MaxConcurrentTurns = 0
				c.MaxQueuedUserMessages = 0
				c.MaxMessageBytes = 0
				c.MaxSystemPromptBytes = 0
				c.MaxStoredMessageBytes = 0
			},
		},
		{
			name:    "negative concurrent turn bound",
			mutate:  func(c *Config) { c.MaxConcurrentTurns = -1 },
			wantErr: true,
		},
		{
			name:    "negative queued message bound",
			mutate:  func(c *Config) { c.MaxQueuedUserMessages = -1 },
			wantErr: true,
		},
		{
			name:    "negative message byte bound",
			mutate:  func(c *Config) { c.MaxMessageBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative system prompt byte bound",
			mutate:  func(c *Config) { c.MaxSystemPromptBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative stored message byte bound",
			mutate:  func(c *Config) { c.MaxStoredMessageBytes = -1 },
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := testConfig(t)
			tc.mutate(&config)

			err := config.Validate()
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}
