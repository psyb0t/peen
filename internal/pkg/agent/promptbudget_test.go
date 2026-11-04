package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	promptBudgetTestRuleName = "oversized"
	// promptBudgetTestRuleWords is more estimated tokens than half the
	// fixture's context window, the default system prompt share.
	promptBudgetTestRuleWords  = runtimeTestMaxContextTokens/2 + 1024
	promptBudgetTestWord       = "word "
	promptBudgetTestScopedHead = "---\npaths: [\"**/*.go\"]\n---\n"
)

// newBudgetedRuntimeFixture is the runtime fixture with the default prompt
// budget, which the shared fixture turns off.
func newBudgetedRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()

	return newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(),
		func(o *RuntimeOptions) { o.MaxSystemPromptPercent = 0 },
	)
}

func writeOversizedRule(t *testing.T, workspace, frontMatter string) {
	t.Helper()

	writeRuntimeFile(
		t,
		filepath.Join(workspace, ".agents", "rules",
			promptBudgetTestRuleName+".md"),
		frontMatter+strings.Repeat(promptBudgetTestWord,
			promptBudgetTestRuleWords),
	)
}

// A rule that loads on every turn and fills most of the context leaves no
// room for the conversation, so the turn is refused before anything is
// stored, and the refusal names the rule to trim.
func TestRuntimeRefusesASystemPromptOverItsBudget(t *testing.T) {
	fixture := newBudgetedRuntimeFixture(t)
	writeOversizedRule(t, fixture.workspace, "")

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "do the thing",
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, harness.ErrPromptBudgetExceeded)
	assert.Contains(t, err.Error(), promptBudgetTestRuleName)
}

// A child agent gets the same rules as its parent, so its prompt is held to
// the same budget.
func TestChildSystemPromptRefusesAPromptOverItsBudget(t *testing.T) {
	fixture := newBudgetedRuntimeFixture(t)
	writeOversizedRule(t, fixture.workspace, "")

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.childSystemPrompt(snapshot, "", fixture.workspace)
	require.ErrorIs(t, err, harness.ErrPromptBudgetExceeded)
}

// A rule scoped to certain files stays out of the system prompt, so it does
// not count against the budget however large it is.
func TestScopedRuleDoesNotCountAgainstThePromptBudget(t *testing.T) {
	fixture := newBudgetedRuntimeFixture(t)
	writeOversizedRule(t, fixture.workspace, promptBudgetTestScopedHead)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.systemPrompt(snapshot, TurnRequest{},
		fixture.workspace)
	require.NoError(t, err)
}

// A negative share turns the check off for an embedder that sizes prompts
// itself.
func TestNegativeSystemPromptPercentTurnsTheBudgetOff(t *testing.T) {
	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(),
		func(o *RuntimeOptions) { o.MaxSystemPromptPercent = -1 },
	)
	writeOversizedRule(t, fixture.workspace, "")

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.systemPrompt(snapshot, TurnRequest{},
		fixture.workspace)
	require.NoError(t, err)
}

func TestSystemPromptTokenLimit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		context int
		percent int
		want    int
	}{
		{name: "half", context: 1000, percent: 50, want: 500},
		{name: "whole", context: 1000, percent: 100, want: 1000},
		{name: "off", context: 1000, percent: -1, want: 0},
		{name: "no context", context: 0, percent: 50, want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want,
				systemPromptTokenLimit(tc.context, tc.percent))
		})
	}
}

// PEEN_MAX_SYSTEM_PROMPT_PERCENT=0 means off, while a zero RuntimeOptions
// field means the default, so the deployment value is translated.
func TestRuntimeSystemPromptPercent(t *testing.T) {
	t.Parallel()

	assert.Negative(t, runtimeSystemPromptPercent(0))
	assert.Equal(t, 40, runtimeSystemPromptPercent(40))
}
