package config

import (
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRefusesTheReservedSystemAgentNames(t *testing.T) {
	testCases := []struct {
		name  string
		agent string
	}{
		{name: "plain", agent: "system"},
		{name: "core", agent: "core:system"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
			t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
			t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
			t.Setenv("PEEN_AGENT", tc.agent)

			_, err := Parse()

			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestParseLeavesTheSystemAgentOffByDefault(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)

	config, err := Parse()

	require.NoError(t, err)
	assert.False(t, config.SystemAgent)
}

func TestParseDefaultsRootAgentToTheCoreAgent(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)

	config, err := Parse()

	require.NoError(t, err)
	assert.Equal(t, "core:general-purpose", config.Agent)
	assert.Empty(t, config.DisabledCoreSkills)
	assert.Empty(t, config.DisabledCoreAgents)
}

func TestParseReadsDisabledCoreSettings(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_AGENT", "reviewer")
	t.Setenv("PEEN_DISABLED_CORE_SKILLS", " planning , freshness ")
	t.Setenv("PEEN_DISABLED_CORE_AGENTS", "all")

	config, err := Parse()
	require.NoError(t, err)

	skills, err := config.DisabledCoreSkillNames()
	require.NoError(t, err)
	assert.Equal(t, []string{"freshness", "planning"}, skills)

	agents, err := config.DisabledCoreAgentNames()
	require.NoError(t, err)
	assert.Equal(t, []string{"general-purpose"}, agents)
}

func TestParseRejectsUnknownDisabledCoreNamesNamingTheEntry(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_DISABLED_CORE_SKILLS", "planning,bogus")

	_, err := Parse()
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "PEEN_DISABLED_CORE_SKILLS")
	assert.Contains(t, err.Error(), "bogus")
	assert.Contains(
		t,
		err.Error(),
		"delegation, freshness, plan-and-delegate, planning",
	)

	t.Setenv("PEEN_DISABLED_CORE_SKILLS", "")
	t.Setenv("PEEN_DISABLED_CORE_AGENTS", "default")

	_, err = Parse()
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "PEEN_DISABLED_CORE_AGENTS")
	assert.Contains(t, err.Error(), "default")
	assert.Contains(t, err.Error(), "general-purpose")
}

func TestParseRefusesRootAgentDisabledAsCore(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_DISABLED_CORE_AGENTS", "all")

	_, err := Parse()

	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "core:general-purpose")
	assert.Contains(t, err.Error(), "PEEN_DISABLED_CORE_AGENTS")
}

func TestParseReadsBlankAgentSettings(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)

	defaults, err := Parse()
	require.NoError(t, err)
	assert.False(t, defaults.CoreInstructionsDisabled())
	assert.False(t, defaults.BaseSystemPromptDisabled())
	assert.Equal(t, DefaultAgent, defaults.RootAgent())

	t.Setenv("PEEN_DISABLE_CORE_INSTRUCTIONS", "true")
	t.Setenv("PEEN_DISABLE_BASE_SYSTEM_PROMPT", "true")

	separate, err := Parse()
	require.NoError(t, err)
	assert.True(t, separate.CoreInstructionsDisabled())
	assert.True(t, separate.BaseSystemPromptDisabled())
	assert.Equal(t, DefaultAgent, separate.RootAgent())
}

func TestParseReadsTheBlankAgentSwitch(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_BLANK_AGENT", "true")

	blank, err := Parse()
	require.NoError(t, err, "the default core agent does not block a blank agent")
	assert.True(t, blank.CoreInstructionsDisabled())
	assert.True(t, blank.BaseSystemPromptDisabled())
	assert.Equal(t, harness.NoRootAgent, blank.RootAgent())

	skills, err := blank.DisabledCoreSkillNames()
	require.NoError(t, err)
	assert.Equal(t, harness.CoreSkillNames(), skills)

	agents, err := blank.DisabledCoreAgentNames()
	require.NoError(t, err)
	assert.Equal(t, harness.CoreAgentNames(), agents)

	t.Setenv("PEEN_AGENT", "reviewer")

	custom, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, "reviewer", custom.RootAgent())
}

func TestParseRejectsAMalformedBlankAgentSwitch(t *testing.T) {
	t.Setenv("PEEN_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("PEEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("PEEN_UPSTREAMS", `[{"name":"aigate","type":"openai"}]`)
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_BLANK_AGENT", "sometimes")

	_, err := Parse()
	require.Error(t, err)
}
