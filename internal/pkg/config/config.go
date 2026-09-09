// Package config loads Peen's deployment configuration from PEEN_ variables.
package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/gonfiguration"
)

// ProviderType selects an Elelem driver implementation.
type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
	ProviderTypeZAICoding ProviderType = "zai-coding"
)

// CompactionMode determines how Peen handles an over-budget transcript.
type CompactionMode string

const (
	CompactionModeDropOldest CompactionMode = "drop-oldest"
	CompactionModeSummarize  CompactionMode = "summarize"
)

// Config is the deployment-owned configuration. Provider credentials stay in
// individually named environment variables, never inside PEEN_UPSTREAMS.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type Config struct {
	ConfigDirectory  string `env:"PEEN_CONFIG_DIR,required"`
	WorkingDirectory string `env:"PEEN_WORKING_DIR"`
	Agent            string `default:"default"              env:"PEEN_AGENT"`

	UpstreamsJSON   string `env:"PEEN_UPSTREAMS,required"`
	DefaultModel    string `env:"PEEN_DEFAULT_MODEL,required"`
	CompactionModel string `env:"PEEN_COMPACTION_MODEL"`

	MaxContextTokens       int            `default:"32768"       env:"PEEN_MAX_CONTEXT_TOKENS"`           //nolint:lll // Immutable env tag.
	CompactionMode         CompactionMode `default:"drop-oldest" env:"PEEN_COMPACTION_MODE"`              //nolint:lll // Immutable env tag.
	CompactionOutputTokens int            `default:"2048"        env:"PEEN_COMPACTION_MAX_OUTPUT_TOKENS"` //nolint:lll // Immutable env tag.
	CompactionTimeout      time.Duration  `default:"2m"          env:"PEEN_COMPACTION_TIMEOUT"`           //nolint:lll // Immutable env tag.
	TurnTimeout            time.Duration  `default:"10m"         env:"PEEN_TURN_TIMEOUT"`                 //nolint:lll // Immutable env tag.

	// MaxConcurrentTurns bounds how many turns may run at once across every
	// session in this process, unlike MaxConcurrentAgentRunsPerSession, which
	// only bounds one session's child-agent runs.
	MaxConcurrentTurns int `default:"16" env:"PEEN_MAX_CONCURRENT_TURNS"` //nolint:lll // Immutable env tag.
	// MaxQueuedUserMessages bounds messages accepted while a session has a
	// running turn. They are delivered at an eligible provider round boundary.
	MaxQueuedUserMessages int `default:"16" env:"PEEN_MAX_QUEUED_USER_MESSAGES"` //nolint:lll // Immutable env tag.

	// MaxMessageBytes bounds a caller-supplied POST /v1/messages "message"
	// field. MaxSystemPromptBytes bounds its optional
	// "systemPrompt.content" field. MaxStoredMessageBytes bounds any single
	// message row, of any role, before it is written to the durable
	// transcript, since a stored assistant or tool message is not otherwise
	// bounded by a caller-facing setting.
	MaxMessageBytes       int `default:"262144"  env:"PEEN_MAX_MESSAGE_BYTES"`        //nolint:lll // Immutable env tag.
	MaxSystemPromptBytes  int `default:"65536"   env:"PEEN_MAX_SYSTEM_PROMPT_BYTES"`  //nolint:lll // Immutable env tag.
	MaxStoredMessageBytes int `default:"1048576" env:"PEEN_MAX_STORED_MESSAGE_BYTES"` //nolint:lll // Immutable env tag.

	MaxToolRounds        int           `default:"32"   env:"PEEN_MAX_TOOL_ROUNDS"`          //nolint:lll // Immutable env tag.
	MaxConcurrentTools   int           `default:"4"    env:"PEEN_MAX_CONCURRENT_TOOLS"`     //nolint:lll // Immutable env tag.
	ToolTimeout          time.Duration `default:"15m"  env:"PEEN_TOOL_TIMEOUT"`             //nolint:lll // Immutable env tag.
	MaxToolResultTokens  int           `default:"8192" env:"PEEN_MAX_TOOL_RESULT_TOKENS"`   //nolint:lll // Immutable env tag.
	EnableWorkspaceHooks bool          `default:"false" env:"PEEN_ENABLE_WORKSPACE_HOOKS"`  //nolint:lll // Immutable env tag.
	HookCommandTimeout   time.Duration `default:"30s" env:"PEEN_HOOK_COMMAND_TIMEOUT"`      //nolint:lll // Immutable env tag.
	MaxHookCommandOutput int           `default:"65536" env:"PEEN_MAX_HOOK_COMMAND_OUTPUT"` //nolint:lll // Immutable env tag.

	ToolMaxListEntries        int           `default:"1000"    env:"PEEN_TOOL_MAX_LIST_ENTRIES"`         //nolint:lll // Immutable env tag.
	ToolMaxListDepth          int           `default:"16"      env:"PEEN_TOOL_MAX_LIST_DEPTH"`           //nolint:lll // Immutable env tag.
	ToolMaxSearchMatches      int           `default:"200"     env:"PEEN_TOOL_MAX_SEARCH_MATCHES"`       //nolint:lll // Immutable env tag.
	ToolMaxSearchFileBytes    int64         `default:"2097152" env:"PEEN_TOOL_MAX_SEARCH_FILE_BYTES"`    //nolint:lll // Immutable env tag.
	ToolMaxReadBytes          int           `default:"262144"  env:"PEEN_TOOL_MAX_READ_BYTES"`           //nolint:lll // Immutable env tag.
	ToolMaxReadLines          int           `default:"2000"    env:"PEEN_TOOL_MAX_READ_LINES"`           //nolint:lll // Immutable env tag.
	ToolMaxWriteBytes         int           `default:"4194304" env:"PEEN_TOOL_MAX_WRITE_BYTES"`          //nolint:lll // Immutable env tag.
	ToolMaxEdits              int           `default:"64"      env:"PEEN_TOOL_MAX_EDITS"`                //nolint:lll // Immutable env tag.
	ToolMaxDiffBytes          int           `default:"65536"   env:"PEEN_TOOL_MAX_DIFF_BYTES"`           //nolint:lll // Immutable env tag.
	ToolMaxRemoveEntries      int           `default:"20000"   env:"PEEN_TOOL_MAX_REMOVE_ENTRIES"`       //nolint:lll // Immutable env tag.
	ToolMaxCommandOutputBytes int           `default:"65536"   env:"PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES"` //nolint:lll // Immutable env tag.
	ToolCommandTimeout        time.Duration `default:"2m"      env:"PEEN_TOOL_COMMAND_TIMEOUT"`          //nolint:lll // Immutable env tag.
	ToolMaxCommandTimeout     time.Duration `default:"15m"     env:"PEEN_TOOL_MAX_COMMAND_TIMEOUT"`      //nolint:lll // Immutable env tag.

	MaxPendingEvents     int `default:"256"   env:"PEEN_MAX_PENDING_EVENTS"`       //nolint:lll // Immutable env tag.
	MaxEventSummaryBytes int `default:"4096"  env:"PEEN_MAX_EVENT_SUMMARY_BYTES"`  //nolint:lll // Immutable env tag.
	MaxEventDataBytes    int `default:"65536" env:"PEEN_MAX_EVENT_DATA_BYTES"`     //nolint:lll // Immutable env tag.
	MaxEventWakesPerHour int `default:"60"    env:"PEEN_MAX_EVENT_WAKES_PER_HOUR"` //nolint:lll // Immutable env tag.

	MaxChildAgentDepth               int `default:"3"     env:"PEEN_MAX_CHILD_AGENT_DEPTH"`             //nolint:lll // Immutable env tag.
	MaxChildAgentTurns               int `default:"16"    env:"PEEN_MAX_CHILD_AGENT_TURNS"`             //nolint:lll // Immutable env tag.
	MaxConcurrentAgentRunsPerSession int `default:"4"     env:"PEEN_MAX_CONCURRENT_AGENT_RUNS"`         //nolint:lll // Immutable env tag.
	MaxAgentRunEventCount            int `default:"2000"  env:"PEEN_MAX_AGENT_RUN_EVENT_COUNT"`         //nolint:lll // Immutable env tag.
	MaxAgentRunEventBytes            int `default:"65536" env:"PEEN_MAX_AGENT_RUN_EVENT_BYTES"`         //nolint:lll // Immutable env tag.
	MaxAdHocAgentInstructionBytes    int `default:"65536" env:"PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES"` //nolint:lll // Immutable env tag.

	HTTPListenAddress    string `default:":8080"           env:"PEEN_HTTP_LISTEN_ADDRESS"`   //nolint:lll // Immutable env tag.
	MetricsListenAddress string `default:"127.0.0.1:9090" env:"PEEN_METRICS_LISTEN_ADDRESS"` //nolint:lll // Immutable env tag.
	APIToken             string `env:"PEEN_API_TOKEN"`
}

// Upstream configures one deployment-owned named provider. The name is Peen's
// stable prefix in qualified model references such as aigate/model-name.
type Upstream struct {
	Name      string       `json:"name"`
	Provider  ProviderType `json:"provider"`
	BaseURL   string       `json:"baseUrl"`
	APIKeyEnv string       `json:"apiKeyEnv"`
}

// Parse reads and validates the fixed PEEN_ environment bindings.
func Parse() (Config, error) {
	config := Config{}
	if err := gonfiguration.Parse(&config); err != nil {
		return Config{}, ctxerrors.Wrap(err, "parse Peen configuration")
	}

	if config.WorkingDirectory == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return Config{}, ctxerrors.Wrap(
				err,
				"resolve default working directory",
			)
		}

		config.WorkingDirectory = workingDirectory
	}

	if err := config.Validate(); err != nil {
		return Config{}, ctxerrors.Wrap(err, "validate Peen configuration")
	}

	return config, nil
}

// Validate checks values which need cross-field or filesystem-aware rules.
func (c Config) Validate() error {
	validators := []func() error{
		c.validateDirectories,
		c.validateRuntime,
		c.validateMetricsListener,
		c.validateUpstreamConfiguration,
		c.validateAgentRunLimits,
		c.validateMessageLimits,
	}
	for _, validate := range validators {
		if err := validate(); err != nil {
			return ctxerrors.Wrap(err, "validate Peen configuration")
		}
	}

	return nil
}

// validateMetricsListener keeps the unauthenticated scrape endpoint private
// to this process's loopback interfaces. The public API has its own listener
// and may be deployed behind an authenticating proxy.
func (c Config) validateMetricsListener() error {
	host, port, err := net.SplitHostPort(c.MetricsListenAddress)
	if err != nil || host == "" || port == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_METRICS_LISTEN_ADDRESS must be host:port",
		)
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_METRICS_LISTEN_ADDRESS must use a loopback IP address",
		)
	}

	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_METRICS_LISTEN_ADDRESS must use a non-zero port",
		)
	}

	return nil
}

func (c Config) validateDirectories() error {
	if !filepath.IsAbs(c.ConfigDirectory) || c.ConfigDirectory == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_CONFIG_DIR must be absolute",
		)
	}

	if c.WorkingDirectory != "" && !filepath.IsAbs(c.WorkingDirectory) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_WORKING_DIR must be absolute",
		)
	}

	return nil
}

func (c Config) validateRuntime() error {
	if strings.TrimSpace(c.Agent) == "" ||
		strings.TrimSpace(c.DefaultModel) == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"agent and default model are required",
		)
	}

	if c.MaxContextTokens <= 0 ||
		c.CompactionOutputTokens <= 0 ||
		c.CompactionOutputTokens >= c.MaxContextTokens ||
		c.CompactionTimeout <= 0 ||
		c.TurnTimeout <= 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid Peen runtime limits")
	}

	if c.CompactionMode != CompactionModeDropOldest &&
		c.CompactionMode != CompactionModeSummarize {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid PEEN_COMPACTION_MODE")
	}

	return c.validateToolLimits()
}

// validateToolLimits rejects a bound that cannot bound anything. These are
// resource limits, never permission gates.
//
// Zero means "not set, take the package default", matching tools.Limits and
// agent.RuntimeOptions, so a caller embedding the runtime does not have to
// restate every bound. Env-driven deployments never see zero because the
// `default:` tags fill them during Parse. Only a negative bound, or a pair
// that cannot hold together, is an error.
func (c Config) validateToolLimits() error {
	bounds := []int{
		c.MaxToolRounds,
		c.MaxConcurrentTools,
		c.MaxToolResultTokens,
		c.MaxHookCommandOutput,
		c.ToolMaxListEntries,
		c.ToolMaxListDepth,
		c.ToolMaxSearchMatches,
		c.ToolMaxReadBytes,
		c.ToolMaxReadLines,
		c.ToolMaxWriteBytes,
		c.ToolMaxEdits,
		c.ToolMaxDiffBytes,
		c.ToolMaxRemoveEntries,
		c.ToolMaxCommandOutputBytes,
		c.MaxPendingEvents,
		c.MaxEventSummaryBytes,
		c.MaxEventDataBytes,
		c.MaxEventWakesPerHour,
	}

	for _, value := range bounds {
		if value < 0 {
			return ctxerrors.Wrap(ErrInvalidConfig, "tool bound is negative")
		}
	}

	if c.ToolMaxSearchFileBytes < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid tool search bound")
	}

	return c.validateToolTimeouts()
}

//nolint:cyclop // Each timeout relationship needs an explicit validation error.
func (c Config) validateToolTimeouts() error {
	if c.ToolTimeout < 0 ||
		c.ToolCommandTimeout < 0 ||
		c.ToolMaxCommandTimeout < 0 ||
		c.HookCommandTimeout < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "tool timeout is negative")
	}

	if c.ToolCommandTimeout > 0 && c.ToolMaxCommandTimeout > 0 &&
		c.ToolCommandTimeout > c.ToolMaxCommandTimeout {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"default command timeout exceeds the maximum",
		)
	}

	// The harness bounds a whole tool call, so a command can never be allowed
	// a longer timeout than the harness will wait. Otherwise a command that
	// asked for the maximum would be killed by the harness first and report a
	// confusing failure instead of its own timeout.
	if c.ToolTimeout > 0 && c.ToolMaxCommandTimeout > c.ToolTimeout {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"maximum command timeout exceeds the tool timeout",
		)
	}

	if c.ToolTimeout > 0 && c.HookCommandTimeout > c.ToolTimeout {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"hook command timeout exceeds the tool timeout",
		)
	}

	return nil
}

func (c Config) validateUpstreamConfiguration() error {
	if _, err := c.Upstreams(); err != nil {
		return ctxerrors.Wrap(err, "validate Peen upstream configuration")
	}

	return nil
}

// validateAgentRunLimits rejects a bound that cannot bound anything, exactly
// like validateToolLimits: zero means "not set, take the package default",
// so only a negative value is an error.
func (c Config) validateAgentRunLimits() error {
	bounds := []int{
		c.MaxChildAgentDepth,
		c.MaxChildAgentTurns,
		c.MaxConcurrentAgentRunsPerSession,
		c.MaxAgentRunEventCount,
		c.MaxAgentRunEventBytes,
		c.MaxAdHocAgentInstructionBytes,
	}

	for _, value := range bounds {
		if value < 0 {
			return ctxerrors.Wrap(
				ErrInvalidConfig,
				"agent run bound is negative",
			)
		}
	}

	return nil
}

// validateMessageLimits rejects a bound that cannot bound anything, exactly
// like validateToolLimits: zero means "not set, take the package default",
// so only a negative value is an error.
func (c Config) validateMessageLimits() error {
	bounds := []int{
		c.MaxConcurrentTurns,
		c.MaxQueuedUserMessages,
		c.MaxMessageBytes,
		c.MaxSystemPromptBytes,
		c.MaxStoredMessageBytes,
	}

	for _, value := range bounds {
		if value < 0 {
			return ctxerrors.Wrap(
				ErrInvalidConfig,
				"message or turn bound is negative",
			)
		}
	}

	return nil
}

// Upstreams parses and validates the configured named provider list.
func (c Config) Upstreams() ([]Upstream, error) {
	var upstreams []Upstream
	if err := json.Unmarshal([]byte(c.UpstreamsJSON), &upstreams); err != nil {
		return nil, ctxerrors.Wrap(err, "parse PEEN_UPSTREAMS JSON")
	}

	if len(upstreams) == 0 {
		return nil, ctxerrors.Wrap(
			ErrInvalidUpstream,
			"at least one upstream is required",
		)
	}

	names := make(map[string]struct{}, len(upstreams))
	for index := range upstreams {
		if err := validateUpstream(upstreams[index]); err != nil {
			return nil, ctxerrors.Wrapf(err, "PEEN_UPSTREAMS entry %d", index)
		}

		if _, exists := names[upstreams[index].Name]; exists {
			return nil, ctxerrors.Wrapf(
				ErrInvalidUpstream,
				"duplicate upstream name %q",
				upstreams[index].Name,
			)
		}

		names[upstreams[index].Name] = struct{}{}
	}

	return upstreams, nil
}

// APIKey resolves only the environment variable selected by the operator.
// Empty is legitimate for intentionally keyless endpoints.
func (u Upstream) APIKey() string {
	if u.APIKeyEnv == "" {
		return ""
	}

	return os.Getenv(u.APIKeyEnv)
}

func validateUpstream(upstream Upstream) error {
	if upstream.Name == "" || strings.Contains(upstream.Name, "/") {
		return ctxerrors.Wrap(
			ErrInvalidUpstream,
			"name must be non-empty and contain no slash",
		)
	}

	if upstream.Provider != ProviderTypeOpenAI &&
		upstream.Provider != ProviderTypeAnthropic &&
		upstream.Provider != ProviderTypeZAICoding {
		return ctxerrors.Wrapf(
			ErrInvalidUpstream,
			"unsupported provider %q",
			upstream.Provider,
		)
	}

	if upstream.APIKeyEnv != "" && upstream.APIKey() == "" {
		return ctxerrors.Wrapf(
			ErrInvalidUpstream,
			"environment variable %q is empty or missing",
			upstream.APIKeyEnv,
		)
	}

	return nil
}
