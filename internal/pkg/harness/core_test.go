package harness

import (
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
	testCoreWritingRules     = CorePrefix + embeddedWritingRulesSkillName
	testCoreWritingSkills    = CorePrefix + embeddedWritingSkillsSkillName
	testCoreGeneralPurpose   = CorePrefix + embeddedGeneralPurposeAgentName
	testCustomPlanningBody   = "custom planning content"
	testCustomAgentBody      = "custom general purpose instructions"
	testUnknownCoreName      = "bogus"
	testCustomReviewerAgent  = "reviewer"
	testMissingAgentName     = "missing-agent"
	testCustomAlphaSkillName = "alpha"
)

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

func TestDisabledCoreInstructionsLeaveOnlyLayerInstructions(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgents(t, fixture.workspace, "workspace instructions")

	for _, disabled := range []bool{false, true} {
		snapshot := resolveWithOptions(
			t,
			fixture,
			WithCoreInstructionsDisabled(disabled),
		)

		instructions := snapshot.Instructions()

		sources := make([]string, 0, len(instructions))
		for _, instruction := range instructions {
			sources = append(sources, instruction.Source)
		}

		if disabled {
			assert.NotContains(t, sources, embeddedInstructionSource)
		} else {
			assert.Contains(t, sources, embeddedInstructionSource)
		}

		assert.Contains(t,
			instructionContents(snapshot.Instructions()),
			"workspace instructions",
		)
	}

	resolver, err := NewResolver(
		fixture.configRoot,
		Limits{},
		WithCoreInstructionsDisabled(true),
	)
	require.NoError(t, err)

	directory, err := resolver.ResolveDirectory(fixture.workspace)
	require.NoError(t, err)

	for _, instruction := range directory.Instructions() {
		assert.NotEqual(t, embeddedInstructionSource, instruction.Source)
	}
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

	require.Len(t, byName, 9)
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
			`delegation, freshness, plan-and-delegate, planning, `+
			`writing-rules, writing-skills and all`,
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

func TestCoreNamesAreSortedAndComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, allCoreSkillNames(), CoreSkillNames())
	assert.Equal(t, []string{"general-purpose"}, CoreAgentNames())
}
