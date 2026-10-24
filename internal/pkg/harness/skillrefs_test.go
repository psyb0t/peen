package harness

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRefSkillA     = "ref-alpha"
	testRefSkillB     = "ref-bravo"
	testRefSkillC     = "ref-charlie"
	testRefSkillD     = "ref-delta"
	testRefGhostSkill = "ref-ghost"
	testRefFileBytes  = 400
	testRefBigBody    = 300
)

func TestSkillReferences(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "one reference at the start",
			message: ":planning make a plan",
			want:    []string{"planning"},
		},
		{
			name:    "punctuation and repeated references",
			message: "use :planning, then :freshness. Repeat :planning.",
			want:    []string{"planning", "freshness"},
		},
		{
			name:    "Unicode whitespace before reference",
			message: "inspect :planning",
			want:    []string{"planning"},
		},
		{
			name:    "colon inside a token is ordinary text",
			message: "path:planning and :planning/notes",
			want:    []string{},
		},
		{
			name:    "core reference is one reference",
			message: ":core:planning make a plan",
			want:    []string{"core:planning"},
		},
		{
			name:    "core and plain references of one name differ",
			message: "use :core:planning, then :planning.",
			want:    []string{"core:planning", "planning"},
		},
		{
			name:    "core reference keeps the boundary rules",
			message: ":core:planning/notes and (:core:freshness) and :core:review!",
			want:    []string{"core:review"},
		},
		{
			name:    "core prefix alone is not a reference",
			message: ":core: nothing and :core:",
			want:    []string{},
		},
		{
			name:    "core reference with an invalid name is not a reference",
			message: ":core:Bad and :core:bad_name",
			want:    []string{},
		},
		{
			name:    "only the exact core prefix is recognized",
			message: ":Core:planning and :other:planning and :core:core:planning",
			want:    []string{},
		},
		{
			name:    "core reference needs a start boundary",
			message: "path:core:planning",
			want:    []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, SkillReferences(tc.message))
		})
	}
}

// referenceFixture writes the four skills most reference tests share. The
// bodies name each other as the test needs.
func referenceFixture(
	t *testing.T,
	bodies map[string]string,
	limits Limits,
) Snapshot {
	t.Helper()

	fixture := newResolverFixture(t)
	for name, body := range bodies {
		fixture.writeSkill(t, fixture.workspace, name, name+" skill", body)
	}

	resolver, err := NewResolver(fixture.configRoot, limits)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	return snapshot
}

func deliveryNames(deliveries []SkillDelivery) []string {
	names := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		names = append(names, delivery.Skill.Name)
	}

	return names
}

func TestExpandSkillsFollowsReferencesDepthFirstInReferenceOrder(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "first :" + testRefSkillB + " then :" + testRefSkillD,
		testRefSkillB: "needs :" + testRefSkillC,
		testRefSkillC: "leaf",
		testRefSkillD: "leaf",
	}, Limits{})

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, nil)
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{testRefSkillA, testRefSkillB, testRefSkillC, testRefSkillD},
		deliveryNames(deliveries),
	)
	assert.False(t, deliveries[0].Referenced)
	assert.True(t, deliveries[1].Referenced)
	assert.True(t, deliveries[2].Referenced)
	assert.True(t, deliveries[3].Referenced)
}

func TestExpandSkillsStopsAReferenceCycle(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB,
		testRefSkillB: "back to :" + testRefSkillA,
	}, Limits{})

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, nil)
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{testRefSkillA, testRefSkillB},
		deliveryNames(deliveries),
	)
}

func TestExpandSkillsSkipsReferencedSkillsAlreadyDelivered(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	delivered := map[string]struct{}{testRefSkillB: {}}

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, delivered)
	require.NoError(t, err)

	assert.Equal(t, []string{testRefSkillA}, deliveryNames(deliveries))
}

func TestExpandSkillsNotesAnExplicitRepeatWithoutItsReferences(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	delivered := map[string]struct{}{testRefSkillA: {}}

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, delivered)
	require.NoError(t, err)

	require.Len(t, deliveries, 1)
	assert.Equal(t, testRefSkillA, deliveries[0].Skill.Name)
	assert.True(t, deliveries[0].AlreadyDelivered)
}

func TestExpandSkillsRepeatInOneBatchIsNotANote(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	deliveries, err := snapshot.ExpandSkills(
		[]string{testRefSkillA, testRefSkillB},
		nil,
	)
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{testRefSkillA, testRefSkillB},
		deliveryNames(deliveries),
	)

	for _, delivery := range deliveries {
		assert.False(t, delivery.AlreadyDelivered)
	}
}

func TestExpandSkillsTreatsPlainAndCoreSpellingsAsOneSkill(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(t, newResolverFixture(t))

	deliveries, err := snapshot.ExpandSkills(
		[]string{embeddedPlanningSkillName, testCorePlanningName},
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, []string{testCorePlanningName}, deliveryNames(deliveries))
}

func TestExpandSkillsDoesNotChangeTheDeliveredSet(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	delivered := map[string]struct{}{}

	_, err := snapshot.ExpandSkills([]string{testRefSkillA}, delivered)
	require.NoError(t, err)

	assert.Empty(t, delivered)
}

func TestExpandSkillsRejectsAnUnknownNamedSkill(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "leaf",
	}, Limits{})

	deliveries, err := snapshot.ExpandSkills(
		[]string{testRefSkillA, testRefGhostSkill},
		nil,
	)
	require.ErrorIs(t, err, ErrSkillNotFound)
	assert.Contains(t, err.Error(), testRefGhostSkill)
	assert.Empty(t, deliveries)
}

func TestExpandSkillsSkipsAnUnknownReferenceInsideASkill(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefGhostSkill + " and :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, nil)
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{testRefSkillA, testRefSkillB},
		deliveryNames(deliveries),
	)
}

func TestExpandSkillsBoundsTheTotalDeliveredContent(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", testRefBigBody)
	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefSkillB + "\n" + big,
		testRefSkillB: big,
	}, Limits{MaxFileBytes: testRefFileBytes})

	single, err := snapshot.ExpandSkills([]string{testRefSkillB}, nil)
	require.NoError(t, err)
	assert.Len(t, single, 1)

	deliveries, err := snapshot.ExpandSkills([]string{testRefSkillA}, nil)
	require.ErrorIs(t, err, ErrSkillContentTooLarge)
	assert.Empty(t, deliveries)
}

func TestNewUnknownSkillErrorNamesTheSkillAndListsWhatExists(t *testing.T) {
	t.Parallel()

	err := NewUnknownSkillError("ghost", []string{"a", "b", "core:planning"})

	assert.Equal(
		t,
		`unknown skill "ghost" in :ghost; available skills: a, b, core:planning`,
		err.Error(),
	)
	require.ErrorIs(t, err, ErrSkillNotFound)
}

func TestNewUnknownSkillErrorTruncatesALongList(t *testing.T) {
	t.Parallel()

	available := make([]string, 0, maxListedSkills+5)
	for index := range maxListedSkills + 5 {
		available = append(available, fmt.Sprintf("skill-%02d", index))
	}

	message := NewUnknownSkillError("ghost", available).Error()

	assert.Contains(t, message, "skill-00")
	assert.NotContains(
		t,
		message,
		fmt.Sprintf("skill-%02d", maxListedSkills),
	)
	assert.True(t, strings.HasSuffix(message, "and 5 more"), message)
}

func TestNewUnknownSkillErrorWithNoSkills(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		`unknown skill "ghost" in :ghost; no skills are available`,
		NewUnknownSkillError("ghost", nil).Error(),
	)
}

func TestResolveWarnsAboutAnUnknownReferenceInASkillBody(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "see :" + testRefGhostSkill + " and :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	warnings := snapshot.Warnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, SourceKindSkill, warnings[0].Kind)
	assert.Contains(t, warnings[0].Source, testRefSkillA)
	assert.Contains(t, warnings[0].Reason, ":"+testRefGhostSkill)
	// Warnings reach clients, so no Go source location may leak into them.
	assert.NotContains(t, warnings[0].Reason, ".go:")
}

func TestResolveIgnoresReferencesInSkillFrontMatter(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkillDocument(
		t,
		fixture.workspace,
		testRefSkillA,
		"---\nname: "+testRefSkillA+
			"\ndescription: pairs with :"+testRefGhostSkill+"\n---\nbody\n",
	)

	snapshot := resolveFixture(t, fixture)

	assert.Empty(t, snapshot.Warnings())
}

func TestResolveAcceptsReferencesToCoreAndCustomSkills(t *testing.T) {
	t.Parallel()

	snapshot := referenceFixture(t, map[string]string{
		testRefSkillA: "uses :" + embeddedPlanningSkillName + " and :" +
			testCoreFreshnessName + " and :" + testRefSkillB,
		testRefSkillB: "leaf",
	}, Limits{})

	assert.Empty(t, snapshot.Warnings())
}

func TestEmbeddedSkillsReferenceOnlyKnownSkills(t *testing.T) {
	t.Parallel()

	snapshot := resolveWithOptions(t, newResolverFixture(t))

	assert.Empty(t, snapshot.Warnings())
}
