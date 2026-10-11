package harness

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testCorePlanningName     = CorePrefix + "planning"
	testCoreFreshnessName    = CorePrefix + "freshness"
	testCoreDelegationName   = CorePrefix + embeddedDelegationSkillName
	testCorePlanDelegateName = CorePrefix + embeddedPlanAndDelegateSkillName
	testCoreGeneralPurpose   = CorePrefix + embeddedGeneralPurposeAgentName
	testCustomPlanningBody   = "custom planning content"
	testCustomAgentBody      = "custom general purpose instructions"
	testUnknownCoreName      = "bogus"
	testCustomReviewerAgent  = "reviewer"
	testMissingAgentName     = "missing-agent"
	testCustomAlphaSkillName = "alpha"
)

func resolveWithOptions(
	t *testing.T,
	fixture resolverFixture,
	options ...ResolverOption,
) Snapshot {
	t.Helper()

	resolver, err := NewResolver(fixture.configRoot, Limits{}, options...)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	return snapshot
}

func embeddedAssetContent(t *testing.T, asset string) string {
	t.Helper()

	content, err := embeddedHarnessAsset(asset)
	require.NoError(t, err)

	return content
}

// The embedded rules and the planning skill are product text. Users depend on
// the plan location, the trigger, and the hand-offs they state.
func TestEmbeddedPlanningRules(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(t, newResolverFixture(t))

	instructions := snapshot.Instructions()
	require.NotEmpty(t, instructions)
	rules := instructions[0].Content

	planning, err := snapshot.ActivateSkill(testCorePlanningName)
	require.NoError(t, err)

	const location = "`.plans/<UTC datetime>-<scope>.md`"

	for name, text := range map[string]string{
		"AGENTS.md": rules,
		"skill":     planning.Content,
	} {
		assert.Contains(t, text, location, name)
		assert.Contains(t, text, "20261010T120501Z", name)
		assert.NotContains(t, text, ".plan/", name)
		assert.Contains(t, text, ".gitignore", name)
		assert.Contains(t, text, "pure question", name)
		assert.Contains(t, text, "parent", name)
		assert.Contains(t, text, "test plan", name)
	}

	assert.Contains(t, rules, "`core:planning`")
	assert.NotContains(t, rules, "`planning` skill")
}

func TestCoreAndCustomSkillsCoexistUnderOneName(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		embeddedPlanningSkillName,
		"custom planning",
		testCustomPlanningBody,
	)

	snapshot := resolveWithOptions(t, fixture)

	assert.Equal(
		t,
		[]string{
			testCoreDelegationName,
			testCoreFreshnessName,
			testCorePlanDelegateName,
			testCorePlanningName,
			embeddedPlanningSkillName,
		},
		skillNames(snapshot.Skills()),
	)

	custom, err := snapshot.ActivateSkill(embeddedPlanningSkillName)
	require.NoError(t, err)
	assert.Equal(t, embeddedPlanningSkillName, custom.Name)
	assert.Contains(t, custom.Content, testCustomPlanningBody)

	core, err := snapshot.ActivateSkill(testCorePlanningName)
	require.NoError(t, err)
	assert.Equal(t, testCorePlanningName, core.Name)
	assert.NotContains(t, core.Content, testCustomPlanningBody)
	assert.Equal(
		t,
		embeddedAssetContent(
			t,
			embeddedSkillAssetFor(embeddedPlanningSkillName).asset,
		),
		core.Content,
	)
	assert.Equal(
		t,
		embeddedSkillsSourcePrefix+embeddedPlanningSkillName+"/"+skillFileName,
		core.Source,
	)
}

func TestPlainSkillNameFallsBackToCore(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(t, newResolverFixture(t))

	plain, err := snapshot.ActivateSkill(embeddedPlanningSkillName)
	require.NoError(t, err)
	assert.Equal(t, testCorePlanningName, plain.Name)

	prefixed, err := snapshot.ActivateSkill(testCorePlanningName)
	require.NoError(t, err)
	assert.Equal(t, plain.Hash, prefixed.Hash)
}

func TestSkillCatalogueListsCoreBeforeCustom(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		testCustomAlphaSkillName,
		"alpha skill",
		testSkillBody,
	)
	fixture.writeSkill(
		t,
		fixture.workspace,
		embeddedPlanningSkillName,
		"custom planning",
		testCustomPlanningBody,
	)

	catalogue := renderSkillCatalogue(
		resolveWithOptions(t, fixture).Skills(),
	)

	positions := []int{
		strings.Index(catalogue, skillCatalogueCoreHeading),
		strings.Index(catalogue, skillCatalogueItem+testCoreFreshnessName+": "),
		strings.Index(catalogue, skillCatalogueItem+testCorePlanningName+": "),
		strings.Index(catalogue, skillCatalogueCustomHeading),
		strings.Index(
			catalogue,
			skillCatalogueItem+testCustomAlphaSkillName+": ",
		),
		strings.Index(
			catalogue,
			skillCatalogueItem+embeddedPlanningSkillName+": custom planning",
		),
	}

	for _, position := range positions {
		require.GreaterOrEqual(t, position, 0, catalogue)
	}

	assert.True(t, strictlyIncreasing(positions), catalogue)
	assert.True(t, strings.HasPrefix(catalogue, skillCatalogueTitle))
}

func TestSkillCatalogueOmitsEmptyGroups(t *testing.T) {
	t.Parallel()

	coreOnly := renderSkillCatalogue(
		resolveWithOptions(t, newResolverFixture(t)).Skills(),
	)
	assert.Contains(t, coreOnly, skillCatalogueCoreHeading)
	assert.NotContains(t, coreOnly, skillCatalogueCustomHeading)

	customOnly := renderSkillCatalogue([]Skill{
		{Name: testCustomAlphaSkillName, Description: "a", Source: "/a"},
	})
	assert.Contains(t, customOnly, skillCatalogueCustomHeading)
	assert.NotContains(t, customOnly, skillCatalogueCoreHeading)
}

func TestDisabledCoreSkillIsGoneEverywhere(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(
		t,
		newResolverFixture(t),
		WithDisabledCoreSkills([]string{embeddedPlanningSkillName}),
	)

	assert.Equal(
		t,
		[]string{
			testCoreDelegationName,
			testCoreFreshnessName,
			testCorePlanDelegateName,
		},
		skillNames(snapshot.Skills()),
	)

	for _, name := range []string{
		embeddedPlanningSkillName,
		testCorePlanningName,
	} {
		_, err := snapshot.ActivateSkill(name)
		require.ErrorIs(t, err, ErrSkillNotFound, name)
	}

	catalogue := renderSkillCatalogue(snapshot.Skills())
	assert.NotContains(t, catalogue, embeddedPlanningSkillName)
	assert.Contains(t, catalogue, testCoreFreshnessName)

	for _, entry := range snapshot.Manifest() {
		assert.NotEqual(t, testCorePlanningName, entry.Name)
	}
}

func TestDisabledCoreSkillLeavesCustomSkillOfSameName(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		embeddedPlanningSkillName,
		"custom planning",
		testCustomPlanningBody,
	)

	snapshot := resolveWithOptions(
		t,
		fixture,
		WithDisabledCoreSkills([]string{embeddedPlanningSkillName}),
	)

	custom, err := snapshot.ActivateSkill(embeddedPlanningSkillName)
	require.NoError(t, err)
	assert.Contains(t, custom.Content, testCustomPlanningBody)

	_, err = snapshot.ActivateSkill(testCorePlanningName)
	require.ErrorIs(t, err, ErrSkillNotFound)
}

func TestAllCoreSkillsDisabledLeavesNoSkills(t *testing.T) {
	t.Parallel()

	disabled, err := ParseDisabledCoreSkills("all")
	require.NoError(t, err)

	snapshot := resolveWithOptions(
		t,
		newResolverFixture(t),
		WithDisabledCoreSkills(disabled),
	)

	assert.Empty(t, snapshot.Skills())
	assert.Equal(t, skillCatalogueEmpty, renderSkillCatalogue(snapshot.Skills()))
}

func TestParseDisabledCoreSelection(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		parse      func(string) ([]string, error)
		raw        string
		want       []string
		wantErr    bool
		wantInText []string
	}{
		{
			name:  "empty disables nothing",
			parse: ParseDisabledCoreSkills,
			raw:   "",
		},
		{
			name:  "blank disables nothing",
			parse: ParseDisabledCoreSkills,
			raw:   "  \t ",
		},
		{
			name:  "names are trimmed sorted and deduplicated",
			parse: ParseDisabledCoreSkills,
			raw:   " planning , freshness,planning,, ",
			want:  []string{"freshness", "planning"},
		},
		{
			name:  "all selects every core skill",
			parse: ParseDisabledCoreSkills,
			raw:   "all",
			want:  allCoreSkillNames(),
		},
		{
			name:  "all among names selects every core skill",
			parse: ParseDisabledCoreSkills,
			raw:   "planning, all",
			want:  allCoreSkillNames(),
		},
		{
			name:       "unknown skill names the entry and the valid names",
			parse:      ParseDisabledCoreSkills,
			raw:        "planning," + testUnknownCoreName,
			wantErr:    true,
			wantInText: []string{testUnknownCoreName, "freshness", "planning", "all"},
		},
		{
			name:       "prefixed skill name is unknown",
			parse:      ParseDisabledCoreSkills,
			raw:        testCorePlanningName,
			wantErr:    true,
			wantInText: []string{testCorePlanningName, "planning"},
		},
		{
			name:  "agent names parse",
			parse: ParseDisabledCoreAgents,
			raw:   embeddedGeneralPurposeAgentName,
			want:  []string{embeddedGeneralPurposeAgentName},
		},
		{
			name:  "all selects every core agent",
			parse: ParseDisabledCoreAgents,
			raw:   "all",
			want:  []string{embeddedGeneralPurposeAgentName},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.parse(tc.raw)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrUnknownCoreName)

				for _, text := range tc.wantInText {
					assert.Contains(t, err.Error(), text)
				}

				return
			}

			require.NoError(t, err)

			if len(tc.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNewResolverRejectsUnknownDisabledCoreNames(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)

	_, err := NewResolver(
		fixture.configRoot,
		Limits{},
		WithDisabledCoreSkills([]string{testUnknownCoreName}),
	)
	require.ErrorIs(t, err, ErrInvalidOptions)
	require.ErrorIs(t, err, ErrUnknownCoreName)

	_, err = NewResolver(
		fixture.configRoot,
		Limits{},
		WithDisabledCoreAgents([]string{testUnknownCoreName}),
	)
	require.ErrorIs(t, err, ErrInvalidOptions)
	require.ErrorIs(t, err, ErrUnknownCoreName)
}

func TestHashChangesWithDisabledCoreSets(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)

	none := resolveWithOptions(t, fixture)
	again := resolveWithOptions(t, fixture)
	skillOff := resolveWithOptions(
		t,
		fixture,
		WithDisabledCoreSkills([]string{embeddedPlanningSkillName}),
	)
	agentOff := resolveWithOptions(
		t,
		fixture,
		WithDisabledCoreAgents([]string{embeddedGeneralPurposeAgentName}),
	)

	assert.Equal(t, none.Hash(), again.Hash())
	assert.NotEqual(t, none.Hash(), skillOff.Hash())
	assert.NotEqual(t, none.Hash(), agentOff.Hash())
	assert.NotEqual(t, skillOff.Hash(), agentOff.Hash())
}

func TestManifestSeparatesCoreFromCustomEntries(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		embeddedPlanningSkillName,
		"custom planning",
		testCustomPlanningBody,
	)
	fixture.writeAgent(
		t,
		fixture.workspace,
		embeddedGeneralPurposeAgentName,
		"custom",
		testCustomAgentBody,
	)

	manifest := resolveWithOptions(t, fixture).Manifest()

	byName := map[string]ManifestEntry{}

	for _, entry := range manifest {
		if entry.Kind == SourceKindSkill || entry.Kind == SourceKindAgent {
			byName[string(entry.Kind)+" "+entry.Name] = entry
		}
	}

	require.Len(t, byName, 7)
	assert.True(t, strings.HasPrefix(
		byName["skill "+testCorePlanningName].Source,
		embeddedSourcePrefix,
	))
	assert.True(t, strings.HasPrefix(
		byName["agent "+testCoreGeneralPurpose].Source,
		embeddedSourcePrefix,
	))
	assert.False(t, strings.HasPrefix(
		byName["skill "+embeddedPlanningSkillName].Source,
		embeddedSourcePrefix,
	))
	assert.False(t, strings.HasPrefix(
		byName["agent "+embeddedGeneralPurposeAgentName].Source,
		embeddedSourcePrefix,
	))
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

func TestCustomNamesCannotCarryTheCorePrefix(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkillDocument(
		t,
		fixture.workspace,
		"core:extra",
		skillDocument("core:extra", "spoof", testSkillBody),
	)
	fixture.writeAgentDocument(
		t,
		fixture.workspace,
		"core:extra",
		agentDocument("core:extra", "spoof", testAgentBody),
	)

	snapshot := resolveWithOptions(t, fixture)

	_, err := snapshot.ActivateSkill("core:extra")
	require.ErrorIs(t, err, ErrSkillNotFound)

	_, err = snapshot.Agent("core:extra")
	require.ErrorIs(t, err, ErrAgentNotFound)

	assert.ElementsMatch(
		t,
		[]SourceKind{SourceKindSkill, SourceKindAgent},
		warningKinds(snapshot.Warnings()),
	)
}

func TestCustomNamesWithTheCorePrefixAreDroppedWithAWarningReason(
	t *testing.T,
) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkillDocument(
		t,
		fixture.workspace,
		"core:extra",
		skillDocument("core:extra", "spoof", testSkillBody),
	)
	fixture.writeAgentDocument(
		t,
		fixture.workspace,
		"core:extra",
		agentDocument("core:extra", "spoof", testAgentBody),
	)

	warnings := resolveWithOptions(t, fixture).Warnings()
	require.Len(t, warnings, 2)

	reasons := map[SourceKind]string{}

	for _, warning := range warnings {
		reasons[warning.Kind] = warning.Reason

		assert.Contains(t, warning.Source, "core:extra")
	}

	assert.Contains(t, reasons[SourceKindSkill], "validate skill name")
	assert.Contains(t, reasons[SourceKindAgent], "validate named agent name")
}

func TestUnknownDisabledCoreNameMessageIsExact(t *testing.T) {
	t.Parallel()

	_, err := ParseDisabledCoreSkills("bogus")
	require.ErrorIs(t, err, ErrUnknownCoreName)
	assert.Contains(
		t,
		err.Error(),
		`core skill "bogus" does not exist, valid names are `+
			`delegation, freshness, plan-and-delegate, planning and all`,
	)

	_, err = ParseDisabledCoreAgents("bogus")
	require.ErrorIs(t, err, ErrUnknownCoreName)
	assert.Contains(
		t,
		err.Error(),
		`core agent "bogus" does not exist, valid names are `+
			`general-purpose and all`,
	)
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

// allCoreSkillNames is every embedded core skill, sorted.
func allCoreSkillNames() []string {
	return []string{
		embeddedDelegationSkillName,
		embeddedFreshnessSkillName,
		embeddedPlanAndDelegateSkillName,
		embeddedPlanningSkillName,
	}
}

// allCoreSkillRefs is every embedded core skill under its core: name, sorted.
func allCoreSkillRefs() []string {
	return []string{
		testCoreDelegationName,
		testCoreFreshnessName,
		testCorePlanDelegateName,
		testCorePlanningName,
	}
}

func TestCoreNamesAreSortedAndComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, allCoreSkillNames(), CoreSkillNames())
	assert.Equal(t, []string{"general-purpose"}, CoreAgentNames())
}
