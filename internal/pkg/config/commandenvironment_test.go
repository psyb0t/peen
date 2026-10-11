package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A command the agent runs gets the process environment without Peen's own
// secrets: the API token and every provider credential variable named by an
// upstream's apiKeyEnv. Everything else, including the settings the worker
// itself reads, stays.
func TestCommandEnvironmentRemovesSecrets(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.UpstreamsJSON = `[
		{"name":"first","type":"openai","apiKeyEnv":"FIRST_PROVIDER_KEY"},
		{"name":"second","type":"anthropic","apiKeyEnv":"SECOND_PROVIDER_KEY"},
		{"name":"keyless","type":"openai"}
	]`

	environ := []string{
		"PATH=/usr/bin",
		"HOME=/home/agent",
		"PEEN_API_TOKEN=api-token-value",
		"FIRST_PROVIDER_KEY=first-key-value",
		"SECOND_PROVIDER_KEY=second-key-value",
		"PEEN_DEFAULT_MODEL=first/model",
		"UNRELATED=kept=with=equals",
		"MALFORMED_ENTRY",
	}

	got, err := config.CommandEnvironment(environ)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"HOME=/home/agent",
		"PEEN_DEFAULT_MODEL=first/model",
		"UNRELATED=kept=with=equals",
		"MALFORMED_ENTRY",
	}, got)
}

// The input slice is not modified.
func TestCommandEnvironmentLeavesTheInputAlone(t *testing.T) {
	t.Parallel()

	environ := []string{"PEEN_API_TOKEN=secret", "PATH=/usr/bin"}

	_, err := testConfig(t).CommandEnvironment(environ)
	require.NoError(t, err)

	assert.Equal(t, []string{"PEEN_API_TOKEN=secret", "PATH=/usr/bin"}, environ)
}

// Unparseable upstreams cannot name their credential variables, so the
// environment is refused rather than returned with a secret in it.
func TestCommandEnvironmentRefusesUnreadableUpstreams(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.UpstreamsJSON = `not json`

	_, err := config.CommandEnvironment([]string{"PATH=/usr/bin"})
	require.ErrorIs(t, err, ErrInvalidUpstream)
}

// Every secret-carrying PEEN_ setting is on the list. Adding a setting that
// holds a secret to Config means adding it here too.
func TestSecretSettingKeysCoverTheAPIToken(t *testing.T) {
	t.Parallel()

	assert.Contains(t, secretSettingKeys(), "PEEN_API_TOKEN")
}
