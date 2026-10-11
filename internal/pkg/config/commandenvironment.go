package config

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

// secretSettingKeys lists the PEEN_ settings whose value is a secret. A new
// setting that carries a secret goes here, so no command the agent runs
// inherits it.
func secretSettingKeys() []string {
	return []string{environmentAPIToken}
}

// CommandEnvironment returns environ without Peen's own secrets, for the
// commands a session's agent runs.
//
// A worker holds the controller's API token and the provider credentials its
// model calls need. A command the agent runs, such as `env`, would print them
// into the stored transcript and the live feed, and the API token would let the
// agent drive every other session. So the token, every variable an upstream
// names with apiKeyEnv, and any other secret-carrying setting are removed. The
// worker process itself keeps its environment.
//
// It fails when PEEN_UPSTREAMS cannot be read, because the credential variables
// cannot be named then, and a command environment that might hold a key is
// refused rather than returned.
func (c Config) CommandEnvironment(environ []string) ([]string, error) {
	withheld := secretSettingKeys()

	var upstreams []Upstream
	if err := json.Unmarshal([]byte(c.UpstreamsJSON), &upstreams); err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidUpstream, err),
			"read provider credential variables from PEEN_UPSTREAMS",
		)
	}

	for _, upstream := range upstreams {
		if upstream.APIKeyEnv != "" {
			withheld = append(withheld, upstream.APIKeyEnv)
		}
	}

	kept := make([]string, 0, len(environ))

	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains(withheld, key) {
			continue
		}

		kept = append(kept, entry)
	}

	return kept, nil
}
