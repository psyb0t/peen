package agent

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const (
	// promptBudgetReportedSections is how many of the largest prompt sections
	// a refusal names, so an operator sees what to trim first.
	promptBudgetReportedSections = 3
	promptBudgetSectionSeparator = ", "
)

// systemPromptTokenLimit is the share of maxContextTokens a system prompt may
// take. A percent at or below zero turns the check off.
func systemPromptTokenLimit(maxContextTokens, percent int) int {
	if maxContextTokens <= 0 || percent <= 0 {
		return 0
	}

	return maxContextTokens * percent / percentDenominator
}

// runtimeSystemPromptPercent maps PEEN_MAX_SYSTEM_PROMPT_PERCENT, where zero
// means off, onto RuntimeOptions, where zero means the package default.
func runtimeSystemPromptPercent(deployment int) int {
	if deployment == 0 {
		return -1
	}

	return deployment
}

// promptSectionSize is one prompt block and its estimated token count.
type promptSectionSize struct {
	name   string
	tokens int
}

// checkSystemPromptBudget refuses an assembled system prompt whose estimated
// token count is over the runtime's limit. The estimate uses elelem's default
// tokenizer, the same one compaction falls back to, so it is close to, not
// equal to, what a provider bills.
func (r *Runtime) checkSystemPromptBudget(
	systemPrompt string,
	blocks []harness.PromptBlock,
) error {
	if r.systemPromptTokenLimit <= 0 {
		return nil
	}

	tokens, err := estimateTextTokens(systemPrompt)
	if err != nil {
		return ctxerrors.Wrap(err, "estimate system prompt tokens")
	}

	if tokens <= r.systemPromptTokenLimit {
		return nil
	}

	largest, err := largestPromptSections(blocks)
	if err != nil {
		return err
	}

	return ctxerrors.Wrapf(
		harness.ErrPromptBudgetExceeded,
		"system prompt is about %d tokens, over the %d token limit; "+
			"largest sections: %s",
		tokens,
		r.systemPromptTokenLimit,
		largest,
	)
}

func estimateTextTokens(text string) (int, error) {
	tokens, err := elelem.DefaultTokenCounter().Count([]elelem.Message{{
		Role:    elelem.RoleSystem,
		Content: elelem.Text(text),
	}}, nil)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count tokens")
	}

	return tokens, nil
}

// promptSectionName names a block by its kind and its name, or its source
// file for an instruction block, which has no name.
func promptSectionName(block harness.PromptBlock) string {
	label := cmp.Or(block.Name, block.Source)
	if label == "" {
		return string(block.Kind)
	}

	return string(block.Kind) + " " + label
}

// largestPromptSections names the biggest harness blocks with their estimated
// sizes, largest first.
func largestPromptSections(blocks []harness.PromptBlock) (string, error) {
	sizes := make([]promptSectionSize, 0, len(blocks))
	for _, block := range blocks {
		tokens, err := estimateTextTokens(block.Content)
		if err != nil {
			return "", ctxerrors.Wrap(err, "estimate prompt section tokens")
		}

		sizes = append(sizes, promptSectionSize{
			name:   promptSectionName(block),
			tokens: tokens,
		})
	}

	slices.SortStableFunc(sizes, func(a, b promptSectionSize) int {
		return cmp.Compare(b.tokens, a.tokens)
	})

	names := make([]string, 0, promptBudgetReportedSections)
	for _, size := range sizes[:min(len(sizes), promptBudgetReportedSections)] {
		names = append(names, size.name+" ("+strconv.Itoa(size.tokens)+")")
	}

	return strings.Join(names, promptBudgetSectionSeparator), nil
}
