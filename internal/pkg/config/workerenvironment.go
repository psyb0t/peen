package config

import (
	"os"
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
)

// WorkerEnvironment returns exactly the deployment settings a worker needs.
//
// A Docker worker is a separate process with an intentionally small
// environment. It needs the same runtime limits, model selection, provider
// definitions, and named provider credentials as a native worker, but it must
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

	return environment, nil
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
		"PEEN_UPSTREAMS",
		"PEEN_DEFAULT_MODEL",
		"PEEN_COMPACTION_MODEL",
		"PEEN_MAX_CONTEXT_TOKENS",
		"PEEN_COMPACTION_MODE",
		"PEEN_COMPACTION_MAX_OUTPUT_TOKENS",
		"PEEN_COMPACTION_TIMEOUT",
		"PEEN_TURN_TIMEOUT",
		"PEEN_MAX_CONCURRENT_TURNS",
		"PEEN_MAX_QUEUED_USER_MESSAGES",
		"PEEN_MAX_MESSAGE_BYTES",
		"PEEN_MAX_SYSTEM_PROMPT_BYTES",
		"PEEN_MAX_STORED_MESSAGE_BYTES",
		"PEEN_MAX_TOOL_ROUNDS",
		"PEEN_MAX_CONCURRENT_TOOLS",
		"PEEN_TOOL_TIMEOUT",
		"PEEN_MAX_TOOL_RESULT_TOKENS",
		"PEEN_ENABLE_WORKSPACE_HOOKS",
		"PEEN_HOOK_COMMAND_TIMEOUT",
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
		"PEEN_TOOL_COMMAND_TIMEOUT",
		"PEEN_TOOL_MAX_COMMAND_TIMEOUT",
		"PEEN_MAX_PENDING_EVENTS",
		"PEEN_MAX_EVENT_SUMMARY_BYTES",
		"PEEN_MAX_EVENT_DATA_BYTES",
		"PEEN_MAX_EVENT_WAKES_PER_HOUR",
		"PEEN_MAX_CHILD_AGENT_DEPTH",
		"PEEN_MAX_CHILD_AGENT_TURNS",
		"PEEN_MAX_CONCURRENT_AGENT_RUNS",
		"PEEN_MAX_AGENT_RUN_EVENT_COUNT",
		"PEEN_MAX_AGENT_RUN_EVENT_BYTES",
		"PEEN_MAX_ADHOC_AGENT_INSTRUCTION_BYTES":
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
		"PEEN_EXECUTION_PROFILES",
		"PEEN_DEFAULT_EXECUTION_PROFILE",
		"PEEN_WORKSPACE_ROOTS",
		"PEEN_HTTP_LISTEN_ADDRESS",
		"PEEN_METRICS_LISTEN_ADDRESS",
		"PEEN_HOST_USERNAME",
		"PEEN_HOST_HOME",
		"PEEN_LOG_DIRECTORY",
		"PEEN_LOG_RETENTION_DAYS":
		return true
	}

	return key == "DOCKER_HOST"
}
