package config

import (
	"regexp"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const mcpEnvSeparator = ","

// environmentVariableNamePattern is a portable environment variable name.
var environmentVariableNamePattern = regexp.MustCompile(
	`^[A-Za-z_][A-Za-z0-9_]*$`,
)

// MCPEnvNames is PEEN_MCP_ENV as a list of variable names.
func (c Config) MCPEnvNames() []string {
	names := make([]string, 0)

	for name := range strings.SplitSeq(c.MCPEnv, mcpEnvSeparator) {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(names, name) {
			continue
		}

		names = append(names, name)
	}

	return names
}

func (c Config) validateMCP() error {
	// Zero takes the default, the same as the other timeouts.
	if c.MCPStartTimeout < 0 {
		return ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_MCP_START_TIMEOUT is negative")
	}

	for _, name := range c.MCPEnvNames() {
		if !environmentVariableNamePattern.MatchString(name) {
			return ctxerrors.Wrapf(ErrInvalidConfig,
				"PEEN_MCP_ENV names an invalid variable %q", name)
		}

		// A Peen setting is not a server secret. Passing one through would
		// hand a worker a controller-only value or let a server read
		// Peen's own configuration.
		if strings.HasPrefix(name, peenSettingPrefix) ||
			workerEnvironmentReserved(name) {
			return ctxerrors.Wrapf(ErrInvalidConfig,
				"PEEN_MCP_ENV may not name the Peen setting %q", name)
		}
	}

	return nil
}

// addWorkerMCPEnvironment passes the PEEN_MCP_ENV variables that are set to
// a Docker worker. One that is not set is left out, and the server that
// names it fails to start with an error that says so.
func addWorkerMCPEnvironment(
	environment map[string]string,
	names []string,
	lookup func(string) (string, bool),
) {
	for _, name := range names {
		value, found := lookup(name)
		if !found || value == "" {
			continue
		}

		environment[name] = value
	}
}
