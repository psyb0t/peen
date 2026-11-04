// Package config loads Peen's deployment configuration from PEEN_ variables.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/gonfiguration"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

// UpstreamType selects an Elelem driver implementation. It names the wire
// protocol an upstream speaks, not the vendor behind it. The vendor is the
// upstream's own Name.
type UpstreamType string

const (
	UpstreamTypeOpenAI    UpstreamType = "openai"
	UpstreamTypeAnthropic UpstreamType = "anthropic"
	UpstreamTypeZAICoding UpstreamType = "zai-coding"
)

// CompactionMode determines how Peen handles an over-budget transcript.
type CompactionMode string

const (
	CompactionModeDropOldest CompactionMode = "drop-oldest"
	CompactionModeSummarize  CompactionMode = "summarize"
)

// DefaultAgent is PEEN_AGENT's default, the core general-purpose agent. It
// must match the default tag on Config.Agent.
const DefaultAgent = harness.CorePrefix + "general-purpose"

// defaultLearnedDirectoryPath is PEEN_LEARNED_DIR's default inside
// PEEN_CONFIG_DIR: the config layer's own learned directory, which every
// session already reads.
const defaultLearnedDirectoryPath = ".agents/learned"

// PEEN_COMPACTION_SUMMARY_PERCENT default and ceiling. A summary above half
// the context leaves too little room for the conversation it makes room for.
const (
	DefaultCompactionSummaryPercent = 30
	MaxCompactionSummaryPercent     = 50
	percentDenominator              = 100
)

// MaxSystemPromptPercent is PEEN_MAX_SYSTEM_PROMPT_PERCENT's ceiling. Zero
// turns the check off.
const MaxSystemPromptPercent = 100

// Timeout variable names that the worker allowlist and the retired-setting
// messages both refer to.
const (
	envModelRequestTimeout       = "PEEN_MODEL_REQUEST_TIMEOUT"
	envDefaultToolCommandTimeout = "PEEN_DEFAULT_TOOL_COMMAND_TIMEOUT"
	envDefaultHookCommandTimeout = "PEEN_DEFAULT_HOOK_COMMAND_TIMEOUT"
	retiredToolMaxCommandHint    = "nothing; a runCommand or waitJob call may ask for any wait" //nolint:lll // One operator-facing phrase.
)

// CrashRecoveryMode decides what happens to work a crashed controller or
// worker left unfinished.
type CrashRecoveryMode string

const (
	// CrashRecoveryPause keeps crashed work paused until a turn resumes it.
	CrashRecoveryPause CrashRecoveryMode = "pause"
	// CrashRecoveryResume lets the next worker continue crashed work by
	// itself, for deployments that run without anyone watching.
	CrashRecoveryResume CrashRecoveryMode = "resume"
)

// Config is the deployment-owned configuration. Provider credentials stay in
// individually named environment variables, never inside PEEN_UPSTREAMS.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type Config struct {
	// ConfigDirectory is the global configuration and harness directory. It is
	// the one directory every worker receives, read-only, so it holds only what
	// a worker is meant to read: AGENTS.md, .agents, SYSTEM.md,
	// APPEND_SYSTEM.md, and COMPACTION.md. Controller runtime state never lives
	// here, because mounting this directory into a worker hands that worker
	// everything inside it.
	ConfigDirectory string `env:"PEEN_CONFIG_DIR,required"`

	// StateDirectory is the controller's own durable runtime state: SQLite,
	// audit logs, worker socket roots, and controller-only credentials. No
	// worker ever receives it. Keeping it out of ConfigDirectory is what stops
	// a Docker worker from reading another session's transcript through its
	// configuration mount.
	//
	// The tag carries no `required` because Parse and ParseWorker need
	// different answers and gonfiguration decides that at parse time. A
	// controller requires it through Validate. A worker owns no durable state,
	// so ParseWorker leaves it empty and nothing reads it.
	StateDirectory string `env:"PEEN_STATE_DIR"`

	WorkingDirectory string

	// Agent is the root agent. A plain name picks a custom agent first and a
	// core agent second; a core: name picks only the core agent.
	Agent string `default:"core:general-purpose" env:"PEEN_AGENT"`

	// DisabledCoreSkills and DisabledCoreAgents list the embedded skills and
	// agents to remove, as comma-separated names without the core: prefix or
	// "all". They stay strings so a worker, which reads the same variables,
	// parses them with the same rules as the controller.
	DisabledCoreSkills string `env:"PEEN_DISABLED_CORE_SKILLS"`
	DisabledCoreAgents string `env:"PEEN_DISABLED_CORE_AGENTS"`

	// DisableCoreInstructions leaves Peen's embedded AGENTS.md out of the
	// system prompt. DisableBaseSystemPrompt leaves out the embedded base
	// prompt, which SYSTEM.md would otherwise replace. BlankAgent sets both,
	// disables every core skill and agent, and runs the root with no agent
	// instructions unless PEEN_AGENT names a custom agent.
	DisableCoreInstructions bool `default:"false" env:"PEEN_DISABLE_CORE_INSTRUCTIONS"`  //nolint:lll // Immutable env tag.
	DisableBaseSystemPrompt bool `default:"false" env:"PEEN_DISABLE_BASE_SYSTEM_PROMPT"` //nolint:lll // Immutable env tag.
	BlankAgent              bool `default:"false" env:"PEEN_BLANK_AGENT"`

	// AutoEvolve lets the agent write and update its own skills and rules in
	// the workspace's learned directory. LearnedDirectory is the global one,
	// every session's learned layer, defaulting to .agents/learned in
	// PEEN_CONFIG_DIR. Docker workers read it, and write it only with
	// WritableLearnedDirectory or WritableConfigDirectory, because a session
	// that writes it changes what every other session loads.
	// WritableConfigDirectory mounts PEEN_CONFIG_DIR writable into Docker
	// workers and tells the agent it may change the files there.
	AutoEvolve               bool   `default:"false" env:"PEEN_AUTO_EVOLVE"`
	LearnedDirectory         string `env:"PEEN_LEARNED_DIR"`
	WritableLearnedDirectory bool   `default:"false" env:"PEEN_WRITABLE_LEARNED_DIR"` //nolint:lll // Immutable env tag.
	WritableConfigDirectory  bool   `default:"false" env:"PEEN_WRITABLE_CONFIG_DIR"`  //nolint:lll // Immutable env tag.

	// MCP starts the MCP servers in .agents/mcp.json in the config directory,
	// and in the workspace too when WorkspaceMCP is set. MCPEnv lists the
	// variables mcp.json may hand to a server through envFrom or
	// bearerTokenEnv: workers receive them and the agent's commands never
	// do. MCPStartTimeout bounds starting one server and listing its tools.
	MCP             bool          `default:"true"  env:"PEEN_MCP"`
	WorkspaceMCP    bool          `default:"false" env:"PEEN_WORKSPACE_MCP"`
	MCPEnv          string        `env:"PEEN_MCP_ENV"`
	MCPStartTimeout time.Duration `default:"30s"   env:"PEEN_MCP_START_TIMEOUT"` //nolint:lll // Immutable env tag.

	UpstreamsJSON   string `env:"PEEN_UPSTREAMS,required"`
	DefaultModel    string `env:"PEEN_DEFAULT_MODEL,required"`
	CompactionModel string `env:"PEEN_COMPACTION_MODEL"`

	// WorkspaceRoot is the absolute directory a client may open session
	// workspaces under: the root itself or any directory below it. It bounds
	// which host directories the control surface will ever expose to a
	// session, so it is deployment configuration and never comes from a
	// client request. Empty uses the process working directory.
	WorkspaceRoot string `env:"PEEN_WORKSPACE_ROOT"`

	// DockerProfilesJSON defines the named Docker profiles a client may pick,
	// as a JSON array. Every worker capability lives here: image, mounts,
	// Docker access, network, and sudo. None of it ever comes from model
	// output or a client request, which only names an agent space.
	//
	// An empty value defines no profile beyond the built-in `default` one,
	// which has no Docker access, the bridge network, and no sudo.
	DockerProfilesJSON string `env:"PEEN_DOCKER_PROFILES"`

	// DefaultAgentSpace is where a session opened without naming one runs:
	// `control` or `docker:<profile>`. Empty takes `docker:default` when the
	// controller can reach a Docker socket and `control` when it cannot.
	DefaultAgentSpace string `env:"PEEN_DEFAULT_AGENT_SPACE"`

	// WorkerSocketDirectory is the root holding one private directory per
	// session worker. Empty puts it under the state directory, because a
	// socket root is controller runtime state and a worker receives only its
	// own session's directory, never the root.
	WorkerSocketDirectory string `env:"PEEN_WORKER_SOCKET_DIR"`

	// DockerSocket is the controller's own Docker socket, used to create
	// worker containers for a Docker profile. It is the first of three
	// candidates: this setting, a `unix://` DOCKER_HOST, and the platform
	// default. The first one that answers the Docker API launches workers, and
	// any one that answers, or a reachable `tcp://` DOCKER_HOST, counts as
	// Docker authority. Without authority a Docker agent space is refused
	// rather than run in the control agent space. With it, the control agent
	// space is refused, because a control worker could use the daemon to
	// become host root. Setting this to another path does not hide the others.
	DockerSocket string `env:"PEEN_DOCKER_SOCKET"`

	// HostUsername and HostHome describe the host account when a controller
	// itself runs in Docker as an arbitrary numeric UID and GID. In that case
	// the controller container has no passwd entry for the host account, but a
	// Docker worker still has to recreate the account before it can safely
	// write workspace files with host ownership. They are both empty for a
	// controller on the host, which resolves its account from the operating
	// system.
	HostUsername string `env:"PEEN_HOST_USERNAME"`
	HostHome     string `env:"PEEN_HOST_HOME"`

	// WorkerImage overrides the image for every Docker profile. Empty leaves
	// each profile's image in effect. Any reference the Docker daemon resolves,
	// including a local development image, is valid.
	WorkerImage string `env:"PEEN_WORKER_IMAGE"`

	MaxContextTokens         int            `default:"32768"       env:"PEEN_MAX_CONTEXT_TOKENS"`           //nolint:lll // Immutable env tag.
	OutputReserveTokens      int            `default:"4096"        env:"PEEN_OUTPUT_RESERVE_TOKENS"`        //nolint:lll // Immutable env tag.
	CompactionMode           CompactionMode `default:"drop-oldest" env:"PEEN_COMPACTION_MODE"`              //nolint:lll // Immutable env tag.
	CompactionOutputTokens   int            `default:"16384"       env:"PEEN_COMPACTION_MAX_OUTPUT_TOKENS"` //nolint:lll // Immutable env tag.
	CompactionSummaryPercent int            `default:"30"          env:"PEEN_COMPACTION_SUMMARY_PERCENT"`   //nolint:lll // Immutable env tag.
	// MaxSystemPromptPercent refuses a turn whose assembled system prompt,
	// estimated in tokens, is over this share of MaxContextTokens. Zero turns
	// the check off.
	MaxSystemPromptPercent int `default:"50" env:"PEEN_MAX_SYSTEM_PROMPT_PERCENT"` //nolint:lll // Immutable env tag.
	// ModelRequestTimeout bounds each model request, a turn's rounds and a
	// compaction summary alike. A turn has no overall time limit.
	ModelRequestTimeout time.Duration `default:"10m" env:"PEEN_MODEL_REQUEST_TIMEOUT"` //nolint:lll // Immutable env tag.

	// CrashRecovery applies to every workspace this controller serves. It is
	// controller-only: recovery runs before any worker starts.
	CrashRecovery CrashRecoveryMode `default:"pause" env:"PEEN_CRASH_RECOVERY"`

	// MaxConcurrentTurns bounds how many turns may run at once across every
	// session in this process, unlike MaxConcurrentAgentRunsPerSession, which
	// only bounds one session's child-agent runs.
	MaxConcurrentTurns int `default:"16" env:"PEEN_MAX_CONCURRENT_TURNS"` //nolint:lll // Immutable env tag.
	// MaxQueuedUserMessages bounds messages accepted while a session has a
	// running turn. They are delivered at an eligible provider round boundary.
	MaxQueuedUserMessages int `default:"16" env:"PEEN_MAX_QUEUED_USER_MESSAGES"` //nolint:lll // Immutable env tag.

	// MaxMessageBytes bounds the caller-supplied "message" field in a
	// message.send event. MaxSystemPromptBytes bounds its optional
	// "systemPrompt.content" field. MaxStoredMessageBytes bounds any single
	// message row, of any role, before it is written to the durable
	// transcript, since a stored assistant or tool message is not otherwise
	// bounded by a caller-facing setting.
	MaxMessageBytes int `default:"262144"  env:"PEEN_MAX_MESSAGE_BYTES"` //nolint:lll // Immutable env tag.
	// MaxImagesPerMessage and MaxImageBytes bound the images a message.send
	// frame or one readFile call may carry.
	MaxImagesPerMessage   int `default:"4"       env:"PEEN_MAX_IMAGES_PER_MESSAGE"`   //nolint:lll // Immutable env tag.
	MaxImageBytes         int `default:"5242880" env:"PEEN_MAX_IMAGE_BYTES"`          //nolint:lll // Immutable env tag.
	MaxSystemPromptBytes  int `default:"65536"   env:"PEEN_MAX_SYSTEM_PROMPT_BYTES"`  //nolint:lll // Immutable env tag.
	MaxStoredMessageBytes int `default:"1048576" env:"PEEN_MAX_STORED_MESSAGE_BYTES"` //nolint:lll // Immutable env tag.

	MaxToolRounds      int `default:"32"   env:"PEEN_MAX_TOOL_ROUNDS"`      //nolint:lll // Immutable env tag.
	MaxConcurrentTools int `default:"4"    env:"PEEN_MAX_CONCURRENT_TOOLS"` //nolint:lll // Immutable env tag.
	// ToolTimeout bounds one call of any tool except runCommand and waitJob,
	// which a wait bounds instead.
	ToolTimeout         time.Duration `default:"15m"  env:"PEEN_TOOL_TIMEOUT"`           //nolint:lll // Immutable env tag.
	MaxToolResultTokens int           `default:"8192" env:"PEEN_MAX_TOOL_RESULT_TOKENS"` //nolint:lll // Immutable env tag.
	// MaxTurnTokens stops a turn, or a child agent's run, before a model
	// round once its rounds have billed this many tokens. 0 sets no limit.
	MaxTurnTokens int64 `default:"0" env:"PEEN_MAX_TURN_TOKENS"` //nolint:lll // Immutable env tag.
	// DefaultHookCommandTimeout bounds a command hook action that sets no
	// timeout of its own.
	DefaultHookCommandTimeout time.Duration `default:"30s" env:"PEEN_DEFAULT_HOOK_COMMAND_TIMEOUT"` //nolint:lll // Immutable env tag.
	MaxHookCommandOutput      int           `default:"65536" env:"PEEN_MAX_HOOK_COMMAND_OUTPUT"`    //nolint:lll // Immutable env tag.

	ToolMaxListEntries        int   `default:"1000"    env:"PEEN_TOOL_MAX_LIST_ENTRIES"`         //nolint:lll // Immutable env tag.
	ToolMaxListDepth          int   `default:"16"      env:"PEEN_TOOL_MAX_LIST_DEPTH"`           //nolint:lll // Immutable env tag.
	ToolMaxSearchMatches      int   `default:"200"     env:"PEEN_TOOL_MAX_SEARCH_MATCHES"`       //nolint:lll // Immutable env tag.
	ToolMaxSearchFileBytes    int64 `default:"2097152" env:"PEEN_TOOL_MAX_SEARCH_FILE_BYTES"`    //nolint:lll // Immutable env tag.
	ToolMaxReadBytes          int   `default:"262144"  env:"PEEN_TOOL_MAX_READ_BYTES"`           //nolint:lll // Immutable env tag.
	ToolMaxReadLines          int   `default:"2000"    env:"PEEN_TOOL_MAX_READ_LINES"`           //nolint:lll // Immutable env tag.
	ToolMaxWriteBytes         int   `default:"4194304" env:"PEEN_TOOL_MAX_WRITE_BYTES"`          //nolint:lll // Immutable env tag.
	ToolMaxEdits              int   `default:"64"      env:"PEEN_TOOL_MAX_EDITS"`                //nolint:lll // Immutable env tag.
	ToolMaxDiffBytes          int   `default:"65536"   env:"PEEN_TOOL_MAX_DIFF_BYTES"`           //nolint:lll // Immutable env tag.
	ToolMaxRemoveEntries      int   `default:"20000"   env:"PEEN_TOOL_MAX_REMOVE_ENTRIES"`       //nolint:lll // Immutable env tag.
	ToolMaxCommandOutputBytes int   `default:"65536"   env:"PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES"` //nolint:lll // Immutable env tag.
	ToolMaxFetchBytes         int   `default:"2097152" env:"PEEN_TOOL_MAX_FETCH_BYTES"`          //nolint:lll // Immutable env tag.
	// DefaultToolCommandTimeout is how long runCommand and waitJob wait when a
	// call passes no timeout. A call may ask for any wait, longer or shorter.
	DefaultToolCommandTimeout time.Duration `default:"10m" env:"PEEN_DEFAULT_TOOL_COMMAND_TIMEOUT"` //nolint:lll // Immutable env tag.

	MaxPendingEvents     int `default:"256"   env:"PEEN_MAX_PENDING_EVENTS"`       //nolint:lll // Immutable env tag.
	MaxEventSummaryBytes int `default:"4096"  env:"PEEN_MAX_EVENT_SUMMARY_BYTES"`  //nolint:lll // Immutable env tag.
	MaxEventDataBytes    int `default:"65536" env:"PEEN_MAX_EVENT_DATA_BYTES"`     //nolint:lll // Immutable env tag.
	MaxEventWakesPerHour int `default:"60"    env:"PEEN_MAX_EVENT_WAKES_PER_HOUR"` //nolint:lll // Immutable env tag.

	MaxChildAgentDepth               int `default:"5"     env:"PEEN_MAX_CHILD_AGENT_DEPTH"`             //nolint:lll // Immutable env tag.
	MaxChildAgentTurns               int `default:"16"    env:"PEEN_MAX_CHILD_AGENT_TURNS"`             //nolint:lll // Immutable env tag.
	MaxConcurrentAgentRunsPerSession int `default:"4"     env:"PEEN_MAX_CONCURRENT_AGENT_RUNS"`         //nolint:lll // Immutable env tag.
	MaxAgentRunEventCount            int `default:"2000"  env:"PEEN_MAX_AGENT_RUN_EVENT_COUNT"`         //nolint:lll // Immutable env tag.
	MaxAgentRunEventBytes            int `default:"65536" env:"PEEN_MAX_AGENT_RUN_EVENT_BYTES"`         //nolint:lll // Immutable env tag.
	MaxAdHocAgentInstructionBytes    int `default:"65536" env:"PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES"` //nolint:lll // Immutable env tag.

	AskTimeout time.Duration `default:"0" env:"PEEN_ASK_TIMEOUT"` //nolint:lll // Immutable env tag.

	// Retired settings are read only so startup can refuse them by name and
	// point at what replaced them. See retiredSettings.
	RetiredTurnTimeout           string `env:"PEEN_TURN_TIMEOUT"`
	RetiredCompactionTimeout     string `env:"PEEN_COMPACTION_TIMEOUT"`
	RetiredToolCommandTimeout    string `env:"PEEN_TOOL_COMMAND_TIMEOUT"`
	RetiredToolMaxCommandTimeout string `env:"PEEN_TOOL_MAX_COMMAND_TIMEOUT"`
	RetiredHookCommandTimeout    string `env:"PEEN_HOOK_COMMAND_TIMEOUT"`

	HTTPListenAddress    string `default:":8080"           env:"PEEN_HTTP_LISTEN_ADDRESS"`   //nolint:lll // Immutable env tag.
	MetricsListenAddress string `default:"127.0.0.1:9090" env:"PEEN_METRICS_LISTEN_ADDRESS"` //nolint:lll // Immutable env tag.
	APIToken             string `env:"PEEN_API_TOKEN"`

	// dockerEnv replaces the process environment and platform default the
	// Docker daemon lookup reads. Only tests set it, so a lookup never needs
	// t.Setenv.
	dockerEnv *dockerEnvironment
}

// Upstream configures one deployment-owned named provider. The name is Peen's
// stable prefix in qualified model references such as aigate/model-name, and
// the type is the wire protocol that provider speaks.
type Upstream struct {
	Name      string       `json:"name"`
	Type      UpstreamType `json:"type"`
	BaseURL   string       `json:"baseUrl"`
	APIKeyEnv string       `json:"apiKeyEnv"`
	// ImageInput false marks every model of the upstream as text-only, so
	// Peen refuses images for them. Unset or true leaves it to what the
	// driver reports for each model.
	ImageInput *bool `json:"imageInput,omitempty"`
}

// AcceptsImageInput reports whether the operator left image input to the
// driver. Only an explicit false turns it off.
func (u Upstream) AcceptsImageInput() bool {
	return u.ImageInput == nil || *u.ImageInput
}

// Parse reads and validates the fixed PEEN_ environment bindings for a
// controller. It requires PEEN_STATE_DIR and enforces its separation from
// PEEN_CONFIG_DIR.
func Parse() (Config, error) {
	return parse(Config.Validate)
}

// ParseWorker reads the same bindings for a session worker.
//
// A worker owns no durable state, so PEEN_STATE_DIR is neither required nor
// used. The controller keeps it out of a Docker worker's environment on
// purpose, and a control worker that inherits it still opens no database.
func ParseWorker() (Config, error) {
	parsed, err := parse(Config.ValidateWorker)
	if err != nil {
		return Config{}, err
	}

	// Cleared rather than carried, so nothing downstream can read a controller
	// path out of a worker's configuration.
	parsed.StateDirectory = ""

	return parsed, nil
}

func parse(validate func(Config) error) (Config, error) {
	config := Config{}
	if err := gonfiguration.Parse(&config); err != nil {
		return Config{}, ctxerrors.Wrap(err, "parse Peen configuration")
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return Config{}, ctxerrors.Wrap(
			err,
			"resolve process working directory",
		)
	}

	config.WorkingDirectory = workingDirectory

	if err := validate(config); err != nil {
		return Config{}, ctxerrors.Wrap(err, "validate Peen configuration")
	}

	return config, nil
}

// Validate checks values which need cross-field or filesystem-aware rules. It
// is the controller's contract and requires a state directory.
func (c Config) Validate() error {
	if err := c.validate(true); err != nil {
		return err
	}

	return c.validateAgentSpaces()
}

// ValidateWorker is Validate without the controller-only state directory rules.
func (c Config) ValidateWorker() error {
	return c.validate(false)
}

func (c Config) validate(requireStateDirectory bool) error {
	validators := []func() error{
		c.validateRetiredSettings,
		func() error { return c.validateDirectories(requireStateDirectory) },
		c.validateDockerHostIdentity,
		c.validateRuntime,
		c.validateCrashRecovery,
		c.validateCoreSelection,
		c.validateMetricsListener,
		c.validateUpstreamConfiguration,
		c.validateAgentRunLimits,
		c.validateMessageLimits,
		c.validateMCP,
	}
	for _, validate := range validators {
		if err := validate(); err != nil {
			return ctxerrors.Wrap(err, "validate Peen configuration")
		}
	}

	return nil
}

// LearnedDir is the global learned directory: PEEN_LEARNED_DIR, or
// .agents/learned inside PEEN_CONFIG_DIR.
func (c Config) LearnedDir() string {
	if strings.TrimSpace(c.LearnedDirectory) != "" {
		return filepath.Clean(c.LearnedDirectory)
	}

	return filepath.Join(c.ConfigDirectory, defaultLearnedDirectoryPath)
}

// WorkerLearnedDirectory is the learned directory Docker workers mount:
// LearnedDir when auto-evolve is on, and empty otherwise.
func (c Config) WorkerLearnedDirectory() string {
	if !c.AutoEvolve {
		return ""
	}

	return c.LearnedDir()
}

// maxImagePayloadBytes bounds the base64 size of one message's images
// together. The message crosses the worker socket as one frame, which holds
// at most 32 MiB, so the images leave room for the rest of the turn request.
const maxImagePayloadBytes = 28 << 20

// webSocketFrameOverheadBytes is the room a message.send frame needs beyond
// its message, system prompt, and images: the envelope, field names, and
// settings.
const webSocketFrameOverheadBytes = 64 << 10

// base64Size is the size base64 encoding gives n bytes, rounded up: four
// characters for every three bytes.
func base64Size(n int) int64 {
	const (
		base64InputBlock  = 3
		base64OutputBlock = 4
	)

	return int64((n + base64InputBlock - 1) / base64InputBlock *
		base64OutputBlock)
}

// WebSocketReadLimit is the largest client frame the WebSocket reads: a
// message.send with the largest message, system prompt, and images the
// deployment allows.
func (c Config) WebSocketReadLimit() int64 {
	return int64(c.MaxMessageBytes) + int64(c.MaxSystemPromptBytes) +
		int64(c.MaxImagesPerMessage)*base64Size(c.MaxImageBytes) +
		webSocketFrameOverheadBytes
}

// LoadedLearnedDirectory is the global learned directory sessions load:
// LearnedDir while auto-evolve is on, and the config layer's own
// .agents/learned otherwise, which the harness reads either way.
func (c Config) LoadedLearnedDirectory() string {
	if c.AutoEvolve {
		return c.LearnedDir()
	}

	return filepath.Join(c.ConfigDirectory, defaultLearnedDirectoryPath)
}

// LearnedDirectoryWritable reports whether workers may write the global
// learned directory: auto-evolve is on and the operator opted in with
// PEEN_WRITABLE_LEARNED_DIR or PEEN_WRITABLE_CONFIG_DIR.
func (c Config) LearnedDirectoryWritable() bool {
	return c.AutoEvolve &&
		(c.WritableLearnedDirectory || c.WritableConfigDirectory)
}

// CoreInstructionsDisabled reports whether the embedded AGENTS.md stays out
// of the system prompt.
func (c Config) CoreInstructionsDisabled() bool {
	return c.DisableCoreInstructions || c.BlankAgent
}

// BaseSystemPromptDisabled reports whether the embedded base system prompt
// stays out of the system prompt.
func (c Config) BaseSystemPromptDisabled() bool {
	return c.DisableBaseSystemPrompt || c.BlankAgent
}

// RootAgent is the root agent turns run as. A blank agent whose PEEN_AGENT is
// still the default core agent runs as harness.NoRootAgent instead, because
// PEEN_BLANK_AGENT disables every core agent.
func (c Config) RootAgent() string {
	agent := strings.TrimSpace(c.Agent)
	if c.BlankAgent && agent == DefaultAgent {
		return harness.NoRootAgent
	}

	return agent
}

// DisabledCoreSkillNames returns the core skills PEEN_DISABLED_CORE_SKILLS
// removes, without the core: prefix. A blank agent removes them all.
func (c Config) DisabledCoreSkillNames() ([]string, error) {
	raw := c.DisabledCoreSkills
	if c.BlankAgent {
		raw = harness.DisableAllCore
	}

	names, err := harness.ParseDisabledCoreSkills(raw)
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidConfig, err),
			"invalid PEEN_DISABLED_CORE_SKILLS",
		)
	}

	return names, nil
}

// DisabledCoreAgentNames returns the core agents PEEN_DISABLED_CORE_AGENTS
// removes, without the core: prefix. A blank agent removes them all.
func (c Config) DisabledCoreAgentNames() ([]string, error) {
	raw := c.DisabledCoreAgents
	if c.BlankAgent {
		raw = harness.DisableAllCore
	}

	names, err := harness.ParseDisabledCoreAgents(raw)
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidConfig, err),
			"invalid PEEN_DISABLED_CORE_AGENTS",
		)
	}

	return names, nil
}

// validateCoreSelection fails startup on an unknown disabled core name and on
// a root agent that names a core agent which does not exist or is disabled.
// A plain root agent name is left to the harness, because only the layers
// know whether a custom agent has it.
func (c Config) validateCoreSelection() error {
	if _, err := c.DisabledCoreSkillNames(); err != nil {
		return err
	}

	disabledAgents, err := c.DisabledCoreAgentNames()
	if err != nil {
		return err
	}

	if err := harness.ValidateCoreAgentReference(
		c.RootAgent(),
		disabledAgents,
	); err != nil {
		return ctxerrors.Wrap(
			errors.Join(ErrInvalidConfig, err),
			"invalid PEEN_AGENT",
		)
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

func (c Config) validateDirectories(requireStateDirectory bool) error {
	if !filepath.IsAbs(c.ConfigDirectory) || c.ConfigDirectory == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_CONFIG_DIR must be absolute",
		)
	}

	if requireStateDirectory {
		if !filepath.IsAbs(c.StateDirectory) || c.StateDirectory == "" {
			return ctxerrors.Wrap(
				ErrInvalidConfig,
				"PEEN_STATE_DIR must be absolute",
			)
		}

		if err := c.validateStateSeparation(); err != nil {
			return err
		}
	}

	if c.WorkingDirectory != "" && !filepath.IsAbs(c.WorkingDirectory) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"process working directory must be absolute",
		)
	}

	return c.validateLearnedDirectory(requireStateDirectory)
}

// validateLearnedDirectory keeps the global learned directory, which an
// auto-evolving Docker worker receives writable, away from the directories
// that must stay read-only or private. It may sit inside PEEN_CONFIG_DIR, but
// may not be or contain it, and may not overlap PEEN_STATE_DIR or the worker
// socket root.
func (c Config) validateLearnedDirectory(requireStateDirectory bool) error {
	learned := c.LearnedDir()
	if !filepath.IsAbs(learned) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_LEARNED_DIR must be absolute",
		)
	}

	config := filepath.Clean(c.ConfigDirectory)
	if learned == config || isAncestorPath(learned, config) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_LEARNED_DIR must not be or contain PEEN_CONFIG_DIR",
		)
	}

	if !requireStateDirectory {
		return nil
	}

	for _, private := range []string{c.StateDirectory, c.WorkerSocketRoot()} {
		if PathsOverlap(learned, private) {
			return ctxerrors.Wrap(
				ErrInvalidConfig,
				"PEEN_LEARNED_DIR must not overlap PEEN_STATE_DIR "+
					"or PEEN_WORKER_SOCKET_DIR",
			)
		}
	}

	return nil
}

// PathsOverlap reports whether two absolute paths are the same directory or
// one contains the other, compared after cleaning.
func PathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)

	return first == second ||
		isAncestorPath(first, second) ||
		isAncestorPath(second, first)
}

// validateDockerHostIdentity keeps the two explicit host-identity values
// atomic. Supplying only one would make a Docker-controller deployment look
// configured while its workers could not recreate the host account.
func (c Config) validateDockerHostIdentity() error {
	username := strings.TrimSpace(c.HostUsername)
	home := strings.TrimSpace(c.HostHome)

	if username == "" && home == "" {
		return nil
	}

	if username == "" || home == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_HOST_USERNAME and PEEN_HOST_HOME must be set together",
		)
	}

	if !filepath.IsAbs(home) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_HOST_HOME must be absolute",
		)
	}

	return nil
}

// validateStateSeparation keeps controller runtime state out of the one
// directory every worker receives.
//
// A Docker worker gets PEEN_CONFIG_DIR mounted read-only. If PEEN_STATE_DIR sat
// inside it, or the two were the same path, that mount would carry SQLite and
// the audit logs into the worker, and one session could read every other
// session's transcript. The reverse nesting is refused for the same reason: a
// configuration directory inside the state directory makes the state directory
// the thing a worker would have to receive.
func (c Config) validateStateSeparation() error {
	// The literal paths are compared first, then the canonical ones. A string
	// comparison alone is bypassable with a symlink, and canonicalization alone
	// would accept a literal nesting whose components do not exist yet, so both
	// run and either one refusing is a refusal.
	if err := refuseOverlap(
		filepath.Clean(c.ConfigDirectory),
		filepath.Clean(c.StateDirectory),
	); err != nil {
		return err
	}

	config, err := canonicalDirectory(c.ConfigDirectory)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve PEEN_CONFIG_DIR")
	}

	state, err := canonicalDirectory(c.StateDirectory)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve PEEN_STATE_DIR")
	}

	return refuseOverlap(config, state)
}

// ValidateStateDirectories applies the deployment's symlink-aware overlap
// check to callers that supply configuration directly instead of environment.
func ValidateStateDirectories(config, state string) error {
	return (Config{
		ConfigDirectory: config,
		StateDirectory:  state,
	}).validateStateSeparation()
}

func refuseOverlap(config, state string) error {
	if config == state {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_CONFIG_DIR and PEEN_STATE_DIR must be different directories",
		)
	}

	if isAncestorPath(config, state) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_STATE_DIR must not be inside PEEN_CONFIG_DIR, which every "+
				"worker receives read-only",
		)
	}

	if isAncestorPath(state, config) {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_CONFIG_DIR must not be inside PEEN_STATE_DIR",
		)
	}

	return nil
}

// canonicalDirectory resolves a directory through its symlinks.
//
// A directory Peen has not created yet is normal on a first run, so the deepest
// existing ancestor is resolved and the remaining literal components are
// rejoined onto it. That still defeats a symlinked parent, which is the case a
// plain string comparison misses: /srv/config/state can be a link into
// /srv/config even though the two strings share no prefix.
//
// Any other resolution failure refuses rather than falling back to the literal
// path, because an unreadable component is exactly where a link could hide.
func canonicalDirectory(path string) (string, error) {
	current := filepath.Clean(path)

	var trailing []string

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(
				append([]string{resolved}, trailing...)...,
			), nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", ctxerrors.Wrapf(
				commerr.ErrParseFailed,
				"cannot resolve %q",
				path,
			)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", ctxerrors.Wrapf(
				commerr.ErrParseFailed,
				"no existing ancestor of %q could be resolved",
				path,
			)
		}

		trailing = append([]string{filepath.Base(current)}, trailing...)
		current = parent
	}
}

// isAncestorPath reports whether descendant sits beneath ancestor. Both paths
// are already cleaned and absolute. The separator guard is what keeps
// /srv/peen-state from counting as a child of /srv/peen.
func isAncestorPath(ancestor, descendant string) bool {
	if ancestor == string(filepath.Separator) {
		return descendant != ancestor
	}

	return strings.HasPrefix(descendant, ancestor+string(filepath.Separator))
}

func (c Config) validateRuntime() error {
	if strings.TrimSpace(c.Agent) == "" ||
		strings.TrimSpace(c.DefaultModel) == "" {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"agent and default model are required",
		)
	}

	if !c.hasContextBudget() || c.ModelRequestTimeout <= 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid Peen runtime limits")
	}

	if c.MaxSystemPromptPercent < 0 ||
		c.MaxSystemPromptPercent > MaxSystemPromptPercent {
		return ctxerrors.Wrapf(
			ErrInvalidConfig,
			"PEEN_MAX_SYSTEM_PROMPT_PERCENT must be 0 to %d",
			MaxSystemPromptPercent,
		)
	}

	if c.CompactionMode != CompactionModeDropOldest &&
		c.CompactionMode != CompactionModeSummarize {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid PEEN_COMPACTION_MODE")
	}

	return c.validateToolLimits()
}

func (c Config) validateCrashRecovery() error {
	if c.CrashRecovery != CrashRecoveryPause &&
		c.CrashRecovery != CrashRecoveryResume {
		return ctxerrors.Wrap(ErrInvalidConfig, "invalid PEEN_CRASH_RECOVERY")
	}

	return nil
}

// ResumesAfterCrash reports whether work a crash interrupted continues on its
// own instead of waiting paused for a turn.
func (c Config) ResumesAfterCrash() bool {
	return c.CrashRecovery == CrashRecoveryResume
}

// hasContextBudget reports whether the context holds the reply's reserve and a
// summary with prompt room to spare. The prompt gets the context minus the
// reserve, and a summary has to fit in what is left.
func (c Config) hasContextBudget() bool {
	if c.MaxContextTokens <= 0 || c.OutputReserveTokens < 0 ||
		c.CompactionOutputTokens <= 0 ||
		c.CompactionSummaryPercent < 0 ||
		c.CompactionSummaryPercent > MaxCompactionSummaryPercent {
		return false
	}

	summary := c.CompactionSummaryTokens()

	return summary > 0 && summary+c.OutputReserveTokens < c.MaxContextTokens
}

// CompactionSummaryTokens is the room one summary gets: its share of the
// context, never more than the configured output ceiling.
func (c Config) CompactionSummaryTokens() int {
	return CompactionSummaryTokens(
		c.MaxContextTokens,
		c.CompactionSummaryPercent,
		c.CompactionOutputTokens,
	)
}

// CompactionSummaryTokens returns percent of contextTokens, capped at
// ceiling. A zero percent takes DefaultCompactionSummaryPercent. A summary
// replaces history, so it is sized against the whole context, and the ceiling
// keeps the request within what the compaction model can write in one reply.
func CompactionSummaryTokens(contextTokens, percent, ceiling int) int {
	if percent == 0 {
		percent = DefaultCompactionSummaryPercent
	}

	return min(contextTokens*percent/percentDenominator, ceiling)
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
		c.ToolMaxFetchBytes,
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

	if c.MaxTurnTokens < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "turn token limit is negative")
	}

	return c.validateToolTimeouts()
}

func (c Config) validateToolTimeouts() error {
	if c.ToolTimeout < 0 ||
		c.DefaultToolCommandTimeout < 0 ||
		c.DefaultHookCommandTimeout < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig, "tool timeout is negative")
	}

	return nil
}

// retiredSetting names a removed variable and the one that replaced it.
type retiredSetting struct {
	name        string
	value       string
	replacement string
}

func (c Config) retiredSettings() []retiredSetting {
	return []retiredSetting{
		{
			"PEEN_TURN_TIMEOUT", c.RetiredTurnTimeout,
			envModelRequestTimeout,
		},
		{
			"PEEN_COMPACTION_TIMEOUT", c.RetiredCompactionTimeout,
			envModelRequestTimeout,
		},
		{
			"PEEN_TOOL_COMMAND_TIMEOUT", c.RetiredToolCommandTimeout,
			envDefaultToolCommandTimeout,
		},
		{
			"PEEN_TOOL_MAX_COMMAND_TIMEOUT", c.RetiredToolMaxCommandTimeout,
			retiredToolMaxCommandHint,
		},
		{
			"PEEN_HOOK_COMMAND_TIMEOUT", c.RetiredHookCommandTimeout,
			envDefaultHookCommandTimeout,
		},
	}
}

// validateRetiredSettings refuses a removed variable that is still set, so a
// deployment does not silently lose the value it configured.
func (c Config) validateRetiredSettings() error {
	for _, setting := range c.retiredSettings() {
		if setting.value == "" {
			continue
		}

		return ctxerrors.Wrapf(ErrInvalidConfig,
			"%s was removed; use %s", setting.name, setting.replacement)
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

	if c.AskTimeout < 0 {
		return ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_ASK_TIMEOUT must not be negative",
		)
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
		c.MaxImagesPerMessage,
		c.MaxImageBytes,
	}

	for _, value := range bounds {
		if value < 0 {
			return ctxerrors.Wrap(
				ErrInvalidConfig,
				"message or turn bound is negative",
			)
		}
	}

	if int64(c.MaxImagesPerMessage)*base64Size(c.MaxImageBytes) >
		maxImagePayloadBytes {
		return ctxerrors.Wrapf(
			ErrInvalidConfig,
			"PEEN_MAX_IMAGES_PER_MESSAGE times PEEN_MAX_IMAGE_BYTES must "+
				"stay under %d bytes of base64",
			maxImagePayloadBytes,
		)
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

// WorkspaceRoots returns the canonical directories a client may open a session
// under. Peen supports one workspace root, so the list holds exactly one
// entry: PEEN_WORKSPACE_ROOT, or the process working directory when it is
// unset. The list shape is what the workspace policy and the HTTP API carry.
//
// The root is resolved through its symlinks, so a request naming an alias of
// it compares equal to it rather than being refused.
func (c Config) WorkspaceRoots() ([]string, error) {
	root := strings.TrimSpace(c.WorkspaceRoot)
	if root == "" {
		root = c.WorkingDirectory
	}

	if !filepath.IsAbs(root) {
		return nil, ctxerrors.Wrap(
			ErrInvalidConfig,
			"PEEN_WORKSPACE_ROOT must be an absolute path",
		)
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve PEEN_WORKSPACE_ROOT")
	}

	return []string{resolved}, nil
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

	if upstream.Type != UpstreamTypeOpenAI &&
		upstream.Type != UpstreamTypeAnthropic &&
		upstream.Type != UpstreamTypeZAICoding {
		return ctxerrors.Wrapf(
			ErrInvalidUpstream,
			"unsupported type %q",
			upstream.Type,
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
