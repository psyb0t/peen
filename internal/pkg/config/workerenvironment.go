package config

import (
	"os"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	// environmentConfigDirectory is supplied by the Docker launcher from its
	// controller-issued launch document. It is not inherited from the control
	// process, so a worker cannot be pointed at another configuration mount.
	environmentConfigDirectory = "PEEN_CONFIG_DIR"

	// environmentStateDirectory and environmentAPIToken belong only to the
	// controller. The state directory carries the database, audit trace, and
	// every worker socket. The API token authorizes the control surface, which
	// a worker reaches through its private credentialed socket instead.
	environmentStateDirectory = "PEEN_STATE_DIR"
	environmentAPIToken       = "PEEN_API_TOKEN" //nolint:gosec // Name only.

	// peenSettingPrefix starts every Peen deployment setting.
	peenSettingPrefix = "PEEN_"

	// The logging library's settings. A worker gets the controller's level and
	// source setting, and always writes JSON, because the controller relays
	// its output and keeps a record's level only when it can parse the line.
	environmentLogLevel     = "LOG_LEVEL"
	environmentLogFormat    = "LOG_FORMAT"
	environmentLogAddSource = "LOG_ADD_SOURCE"
	workerLogFormat         = "json"
)

// WorkerEnvironment returns exactly the deployment settings a worker needs.
//
// A Docker worker is a separate process with an intentionally small
// environment. It needs the same runtime limits, model selection, provider
// definitions, and named provider credentials as a control worker, but it must
// not inherit controller-only paths or authority. The allowlist makes that
// boundary visible and prevents a future PEEN_ control setting from quietly
// becoming a worker capability.
func (c Config) WorkerEnvironment() (map[string]string, error) {
	upstreams, err := c.Upstreams()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load worker upstream configuration")
	}

	environment := workerRuntimeEnvironment(os.Environ())
	if err := addWorkerCredentials(environment, upstreams); err != nil {
		return nil, err
	}

	addWorkerLogSettings(environment)

	return environment, nil
}

// addWorkerLogSettings passes the controller's log level and source setting
// to a worker and makes it write JSON.
func addWorkerLogSettings(environment map[string]string) {
	for _, key := range []string{environmentLogLevel, environmentLogAddSource} {
		if value, found := os.LookupEnv(key); found {
			environment[key] = value
		}
	}

	environment[environmentLogFormat] = workerLogFormat
}

// ControlWorkerEnvironment returns environ without the settings a control
// worker must not inherit from its controller.
//
// It removes every controller-only key (the API token, the state directory,
// Docker settings, listen addresses, host identity, and log settings) and every
// PEEN_ key outside the worker allowlist. A key named in credentialKeys stays
// even when it starts with PEEN_, because it holds a provider credential the
// worker's model calls need, unless it is controller-only. Variables outside
// PEEN_ stay, so the agent's commands keep PATH, HOME, and the user's
// toolchain. PEEN_CONFIG_DIR is controller-only here too: the control launcher
// sets it from the launch document, as the Docker launcher does.
func ControlWorkerEnvironment(environ, credentialKeys []string) []string {
	kept := make([]string, 0, len(environ)+1)

	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if key == environmentLogFormat ||
			!controlWorkerEnvironmentKey(key, credentialKeys) {
			continue
		}

		kept = append(kept, entry)
	}

	// The controller relays a control worker's output too, so it writes JSON
	// whatever format the controller itself uses.
	return append(kept, environmentLogFormat+"="+workerLogFormat)
}

func controlWorkerEnvironmentKey(key string, credentialKeys []string) bool {
	if workerEnvironmentReserved(key) {
		return false
	}

	if !strings.HasPrefix(key, peenSettingPrefix) {
		return true
	}

	return workerRuntimeEnvironmentKey(key) ||
		slices.Contains(credentialKeys, key)
}

func workerRuntimeEnvironment(entries []string) map[string]string {
	environment := make(map[string]string)

	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if found && workerRuntimeEnvironmentKey(key) {
			environment[key] = value
		}
	}

	return environment
}

func addWorkerCredentials(
	environment map[string]string,
	upstreams []Upstream,
) error {
	for _, upstream := range upstreams {
		if upstream.APIKeyEnv == "" {
			continue
		}

		if workerEnvironmentReserved(upstream.APIKeyEnv) ||
			workerRuntimeEnvironmentKey(upstream.APIKeyEnv) {
			return ctxerrors.Wrapf(
				ErrInvalidConfig,
				"provider %q credential variable %q is reserved",
				upstream.Name,
				upstream.APIKeyEnv,
			)
		}

		value, found := os.LookupEnv(upstream.APIKeyEnv)
		if !found || value == "" {
			return ctxerrors.Wrapf(
				ErrInvalidConfig,
				"provider %q credential variable %q is empty",
				upstream.Name,
				upstream.APIKeyEnv,
			)
		}

		environment[upstream.APIKeyEnv] = value
	}

	return nil
}

// workerRuntimeEnvironmentKey is the controller-to-worker configuration
// contract. Keep a new worker runtime setting here when adding one to Config.
func workerRuntimeEnvironmentKey(key string) bool {
	switch key {
	case "PEEN_AGENT",
		"PEEN_DISABLED_CORE_SKILLS",
		"PEEN_DISABLED_CORE_AGENTS",
		"PEEN_UPSTREAMS",
		"PEEN_DEFAULT_MODEL",
		"PEEN_COMPACTION_MODEL",
		"PEEN_MAX_CONTEXT_TOKENS",
		"PEEN_OUTPUT_RESERVE_TOKENS",
		"PEEN_COMPACTION_MODE",
		"PEEN_COMPACTION_MAX_OUTPUT_TOKENS",
		"PEEN_COMPACTION_SUMMARY_PERCENT",
		envModelRequestTimeout,
		"PEEN_MAX_CONCURRENT_TURNS",
		"PEEN_MAX_QUEUED_USER_MESSAGES",
		"PEEN_MAX_MESSAGE_BYTES",
		"PEEN_MAX_SYSTEM_PROMPT_BYTES",
		"PEEN_MAX_STORED_MESSAGE_BYTES",
		"PEEN_MAX_TOOL_ROUNDS",
		"PEEN_MAX_CONCURRENT_TOOLS",
		"PEEN_TOOL_TIMEOUT",
		"PEEN_MAX_TOOL_RESULT_TOKENS",
		envDefaultHookCommandTimeout,
		"PEEN_MAX_HOOK_COMMAND_OUTPUT",
		"PEEN_TOOL_MAX_LIST_ENTRIES",
		"PEEN_TOOL_MAX_LIST_DEPTH",
		"PEEN_TOOL_MAX_SEARCH_MATCHES",
		"PEEN_TOOL_MAX_SEARCH_FILE_BYTES",
		"PEEN_TOOL_MAX_READ_BYTES",
		"PEEN_TOOL_MAX_READ_LINES",
		"PEEN_TOOL_MAX_WRITE_BYTES",
		"PEEN_TOOL_MAX_EDITS",
		"PEEN_TOOL_MAX_DIFF_BYTES",
		"PEEN_TOOL_MAX_REMOVE_ENTRIES",
		"PEEN_TOOL_MAX_COMMAND_OUTPUT_BYTES",
		envDefaultToolCommandTimeout,
		"PEEN_MAX_PENDING_EVENTS",
		"PEEN_MAX_EVENT_SUMMARY_BYTES",
		"PEEN_MAX_EVENT_DATA_BYTES",
		"PEEN_MAX_EVENT_WAKES_PER_HOUR",
		"PEEN_MAX_CHILD_AGENT_DEPTH",
		"PEEN_MAX_CHILD_AGENT_TURNS",
		"PEEN_MAX_CONCURRENT_AGENT_RUNS",
		"PEEN_MAX_AGENT_RUN_EVENT_COUNT",
		"PEEN_MAX_AGENT_RUN_EVENT_BYTES",
		"PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES",
		"PEEN_ASK_TIMEOUT":
		return true
	}

	return false
}

// workerEnvironmentReserved is shared with the Docker launch builder's
// defense in depth. These keys are controlled by the controller, not copied
// into a worker from its parent process or an upstream credential definition.
func workerEnvironmentReserved(key string) bool {
	if strings.HasPrefix(key, "PEEN_WORKER_") {
		return true
	}

	switch key {
	case environmentConfigDirectory,
		environmentStateDirectory,
		environmentAPIToken,
		"PEEN_DOCKER_SOCKET",
		"PEEN_WORKER_IMAGE",
		envDockerProfiles,
		envDefaultAgentSpace,
		"PEEN_WORKSPACE_ROOT",
		"PEEN_HTTP_LISTEN_ADDRESS",
		"PEEN_METRICS_LISTEN_ADDRESS",
		"PEEN_HOST_USERNAME",
		"PEEN_HOST_HOME",
		"PEEN_LOG_DIRECTORY",
		"PEEN_LOG_RETENTION":
		return true
	}

	return key == "DOCKER_HOST"
}
