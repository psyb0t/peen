package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactionSummaryTokens(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		contextTokens int
		percent       int
		ceiling       int
		want          int
	}{
		{
			name:          "share of the context",
			contextTokens: 200000,
			percent:       20,
			ceiling:       100000,
			want:          40000,
		},
		{
			name:          "ceiling caps the share",
			contextTokens: 1000000,
			percent:       30,
			ceiling:       16384,
			want:          16384,
		},
		{
			name:          "zero takes the default share",
			contextTokens: 10000,
			ceiling:       16384,
			want:          10000 * DefaultCompactionSummaryPercent / 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, CompactionSummaryTokens(
				tc.contextTokens,
				tc.percent,
				tc.ceiling,
			))
		})
	}
}

func TestMetricsRetentionDefaultsToSevenDays(t *testing.T) {
	t.Parallel()

	config := testConfig(t)

	config.MetricsRetention = 0
	assert.Equal(t, 7*24*time.Hour, config.MetricsObservationRetention())

	config.MetricsRetention = time.Hour
	assert.Equal(t, time.Hour, config.MetricsObservationRetention())
}

func TestNegativeMetricsRetentionStopsStartup(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.MetricsRetention = -time.Hour

	require.ErrorIs(t, config.Validate(), ErrInvalidConfig)
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
	c.DefaultToolCommandTimeout = 0
	c.DefaultHookCommandTimeout = 0
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
			name:    "negative turn token limit",
			mutate:  func(c *Config) { c.MaxTurnTokens = -1 },
			wantErr: true,
		},
		{
			name:   "turn token limit off",
			mutate: func(c *Config) { c.MaxTurnTokens = 0 },
		},
		{
			name:    "negative search file bound",
			mutate:  func(c *Config) { c.ToolMaxSearchFileBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative command timeout",
			mutate:  func(c *Config) { c.DefaultToolCommandTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "negative hook command timeout",
			mutate:  func(c *Config) { c.DefaultHookCommandTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "negative hook command output limit",
			mutate:  func(c *Config) { c.MaxHookCommandOutput = -1 },
			wantErr: true,
		},
		{
			name: "a hook command default may outlive the tool timeout",
			mutate: func(c *Config) {
				c.ToolTimeout = time.Minute
				c.DefaultHookCommandTimeout = time.Hour
			},
		},
		{
			name: "a command default may outlive the tool timeout",
			mutate: func(c *Config) {
				c.ToolTimeout = time.Minute
				c.DefaultToolCommandTimeout = 2 * time.Hour
			},
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

// Parse reads a removed variable from the environment and refuses it, so a
// deployment that still sets one fails at startup.
func TestParseRefusesARetiredTimeout(t *testing.T) {
	workingDirectory := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.Mkdir(workingDirectory, testDirectoryMode))
	t.Chdir(workingDirectory)
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_AGENT", "coding")
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_TURN_TIMEOUT", "10m")

	_, err := Parse()
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.ErrorContains(t, err, "PEEN_TURN_TIMEOUT")
	assert.ErrorContains(t, err, "PEEN_MODEL_REQUEST_TIMEOUT")
}

// A removed timeout variable still set would otherwise be silently ignored,
// so startup refuses it and names what replaced it.
func TestConfigValidateRefusesRetiredTimeouts(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		set         func(*Config)
		replacement string
	}{
		{
			name:        "PEEN_TURN_TIMEOUT",
			set:         func(c *Config) { c.RetiredTurnTimeout = "10m" },
			replacement: "PEEN_MODEL_REQUEST_TIMEOUT",
		},
		{
			name:        "PEEN_COMPACTION_TIMEOUT",
			set:         func(c *Config) { c.RetiredCompactionTimeout = "2m" },
			replacement: "PEEN_MODEL_REQUEST_TIMEOUT",
		},
		{
			name:        "PEEN_TOOL_COMMAND_TIMEOUT",
			set:         func(c *Config) { c.RetiredToolCommandTimeout = "2m" },
			replacement: "PEEN_DEFAULT_TOOL_COMMAND_TIMEOUT",
		},
		{
			name: "PEEN_TOOL_MAX_COMMAND_TIMEOUT",
			set: func(c *Config) {
				c.RetiredToolMaxCommandTimeout = "15m"
			},
			replacement: "may ask for any wait",
		},
		{
			name:        "PEEN_HOOK_COMMAND_TIMEOUT",
			set:         func(c *Config) { c.RetiredHookCommandTimeout = "30s" },
			replacement: "PEEN_DEFAULT_HOOK_COMMAND_TIMEOUT",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := testConfig(t)
			tc.set(&config)

			err := config.Validate()
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.ErrorContains(t, err, tc.name)
			assert.ErrorContains(t, err, tc.replacement)
		})
	}
}

func TestWebSocketReadLimitHoldsTheLargestMessageSend(t *testing.T) {
	t.Parallel()

	config := Config{
		MaxMessageBytes:      1000,
		MaxSystemPromptBytes: 500,
		MaxImagesPerMessage:  2,
		MaxImageBytes:        3000,
	}

	// Two images of 3,000 bytes are 4,000 base64 characters each.
	assert.Equal(t, int64(1000+500+2*4000+webSocketFrameOverheadBytes),
		config.WebSocketReadLimit())

	config.MaxImageBytes = 3001
	assert.Equal(t, int64(1000+500+2*4004+webSocketFrameOverheadBytes),
		config.WebSocketReadLimit())
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
			name:    "negative system prompt percent",
			mutate:  func(c *Config) { c.MaxSystemPromptPercent = -1 },
			wantErr: true,
		},
		{
			name:    "system prompt percent over the whole context",
			mutate:  func(c *Config) { c.MaxSystemPromptPercent = 101 },
			wantErr: true,
		},
		{
			name:   "system prompt percent check turned off",
			mutate: func(c *Config) { c.MaxSystemPromptPercent = 0 },
		},
		{
			name:    "negative stored message byte bound",
			mutate:  func(c *Config) { c.MaxStoredMessageBytes = -1 },
			wantErr: true,
		},
		{
			name:    "negative image count bound",
			mutate:  func(c *Config) { c.MaxImagesPerMessage = -1 },
			wantErr: true,
		},
		{
			name:    "negative image byte bound",
			mutate:  func(c *Config) { c.MaxImageBytes = -1 },
			wantErr: true,
		},
		{
			name: "default image bounds fit one worker frame",
			mutate: func(c *Config) {
				c.MaxImagesPerMessage = 4
				c.MaxImageBytes = 5 << 20
			},
		},
		{
			name: "image bounds past one worker frame",
			mutate: func(c *Config) {
				c.MaxImagesPerMessage = 6
				c.MaxImageBytes = 5 << 20
			},
			wantErr: true,
		},
		{
			name:   "an ask timeout bounds the wait",
			mutate: func(c *Config) { c.AskTimeout = time.Minute },
		},
		{
			name:    "negative ask timeout",
			mutate:  func(c *Config) { c.AskTimeout = -time.Second },
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
