package harness

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoRootAgentHasNoInstructionsAndNoToolLimit(t *testing.T) {
	t.Parallel()

	snapshot := resolveFixture(t, newResolverFixture(t))

	agent, err := snapshot.Agent(NoRootAgent)
	require.NoError(t, err)
	assert.Equal(t, NoRootAgent, agent.Name)
	assert.Empty(t, agent.Instructions)
	assert.Nil(t, agent.AllowedTools)
	require.NoError(t, ValidateCoreAgentReference(NoRootAgent, nil))
	require.Error(t, validateKebabName(NoRootAgent))

	withAgent, err := snapshot.PromptBlocks(NoRootAgent)
	require.NoError(t, err)

	withoutAgent, err := snapshot.PromptBlocks("")
	require.NoError(t, err)
	assert.Equal(t, withoutAgent, withAgent)
}

func TestEmbeddedAgentIsGeneralPurpose(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "general-purpose", embeddedGeneralPurposeAgentName)

	snapshot := resolveWithOptions(t, newResolverFixture(t))
	assert.Equal(t, []string{testCoreGeneralPurpose}, agentNames(snapshot.Agents()))

	agent, err := snapshot.Agent(testCoreGeneralPurpose)
	require.NoError(t, err)
	assert.Equal(
		t,
		embeddedAgentsSourcePrefix+embeddedGeneralPurposeAgentName+
			agentsFileExtension,
		agent.Source,
	)
}

func TestCoreAndCustomAgentsCoexistUnderOneName(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgent(
		t,
		fixture.configRoot,
		embeddedGeneralPurposeAgentName,
		"custom general purpose",
		testCustomAgentBody,
	)

	snapshot := resolveWithOptions(t, fixture)

	assert.Equal(
		t,
		[]string{testCoreGeneralPurpose, embeddedGeneralPurposeAgentName},
		agentNames(snapshot.Agents()),
	)

	custom, err := snapshot.Agent(embeddedGeneralPurposeAgentName)
	require.NoError(t, err)
	assert.Equal(t, testCustomAgentBody, custom.Instructions)
	assert.Equal(t, filepath.Join(
		fixture.configRoot,
		agentsDirectoryName,
		agentsSubdirectory,
		embeddedGeneralPurposeAgentName+agentsFileExtension,
	), custom.Source)

	core, err := snapshot.Agent(testCoreGeneralPurpose)
	require.NoError(t, err)
	assert.NotEqual(t, testCustomAgentBody, core.Instructions)
	assert.True(t, strings.HasPrefix(core.Source, embeddedSourcePrefix))
}

func TestPlainAgentNameFallsBackToCore(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(t, newResolverFixture(t))

	agent, err := snapshot.Agent(embeddedGeneralPurposeAgentName)
	require.NoError(t, err)
	assert.Equal(t, testCoreGeneralPurpose, agent.Name)
}

func TestAgentCatalogueListsCoreBeforeCustom(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgent(
		t,
		fixture.workspace,
		testCustomReviewerAgent,
		"reviews",
		testCustomAgentBody,
	)

	catalogue := renderAgentCatalogue(resolveWithOptions(t, fixture).Agents())

	positions := []int{
		strings.Index(catalogue, agentCatalogueCoreHeading),
		strings.Index(catalogue, skillCatalogueItem+testCoreGeneralPurpose+": "),
		strings.Index(catalogue, agentCatalogueCustomHeading),
		strings.Index(catalogue, skillCatalogueItem+testCustomReviewerAgent+": "),
	}

	for _, position := range positions {
		require.GreaterOrEqual(t, position, 0, catalogue)
	}

	assert.True(t, strictlyIncreasing(positions), catalogue)
	assert.True(t, strings.HasPrefix(catalogue, agentCatalogueTitle))
}

func TestDisabledCoreAgentIsGone(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(
		t,
		newResolverFixture(t),
		WithDisabledCoreAgents([]string{embeddedGeneralPurposeAgentName}),
	)

	assert.Empty(t, snapshot.Agents())

	for _, name := range []string{
		embeddedGeneralPurposeAgentName,
		testCoreGeneralPurpose,
	} {
		_, err := snapshot.Agent(name)
		require.ErrorIs(t, err, ErrAgentNotFound, name)
	}

	assert.Equal(t, agentCatalogueEmpty, renderAgentCatalogue(snapshot.Agents()))
}

func TestCheckRootAgent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		agent    string
		options  []ResolverOption
		setup    func(t *testing.T, fixture resolverFixture)
		wantErr  bool
		wantText string
	}{
		{name: "core agent", agent: testCoreGeneralPurpose},
		{name: "plain name falls back to core", agent: embeddedGeneralPurposeAgentName},
		{
			name:  "custom agent in the config directory",
			agent: testCustomReviewerAgent,
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()
				fixture.writeAgent(
					t,
					fixture.configRoot,
					testCustomReviewerAgent,
					"reviews",
					testAgentBody,
				)
			},
		},
		{
			// A plain name may be defined only inside a workspace, which
			// startup cannot see, so it resolves per turn instead.
			name:  "plain name absent at startup",
			agent: testMissingAgentName,
		},
		{
			name:     "unknown core name",
			agent:    CorePrefix + testUnknownCoreName,
			wantErr:  true,
			wantText: embeddedGeneralPurposeAgentName,
		},
		{
			name:  "disabled core agent by core name",
			agent: testCoreGeneralPurpose,
			options: []ResolverOption{
				WithDisabledCoreAgents([]string{embeddedGeneralPurposeAgentName}),
			},
			wantErr:  true,
			wantText: "PEEN_DISABLED_CORE_AGENTS",
		},
		{
			// Resolved per turn, where a workspace agent may satisfy it.
			name:  "plain name of a disabled core agent",
			agent: embeddedGeneralPurposeAgentName,
			options: []ResolverOption{
				WithDisabledCoreAgents([]string{embeddedGeneralPurposeAgentName}),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			if tc.setup != nil {
				tc.setup(t, fixture)
			}

			resolver, err := NewResolver(fixture.configRoot, Limits{}, tc.options...)
			require.NoError(t, err)

			err = resolver.CheckRootAgent(tc.agent)
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrAgentNotFound)
			assert.Contains(t, err.Error(), tc.wantText)
		})
	}
}

func TestValidateCoreAgentReference(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateCoreAgentReference(testCoreGeneralPurpose, nil))
	require.NoError(t, ValidateCoreAgentReference(testMissingAgentName, nil))

	err := ValidateCoreAgentReference(
		CorePrefix+testUnknownCoreName,
		nil,
	)
	require.ErrorIs(t, err, ErrAgentNotFound)
	assert.Contains(t, err.Error(), embeddedGeneralPurposeAgentName)

	err = ValidateCoreAgentReference(
		testCoreGeneralPurpose,
		[]string{embeddedGeneralPurposeAgentName},
	)
	require.ErrorIs(t, err, ErrAgentNotFound)
	assert.Contains(t, err.Error(), "PEEN_DISABLED_CORE_AGENTS")
}
