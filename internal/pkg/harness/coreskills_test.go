package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			testCoreWritingRules,
			testCoreWritingSkills,
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
			testCoreWritingRules,
			testCoreWritingSkills,
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
