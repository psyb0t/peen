package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

const (
	// inFlightFitAttempts bounds how often the results are cut again when the
	// request's own counter still finds it over budget.
	inFlightFitAttempts = 4
	// inFlightResultFloorTokens is the least a cut result keeps, so the model
	// still sees what each call returned.
	inFlightResultFloorTokens = 128
	// inFlightMarginDivisor sets the extra room each further attempt frees:
	// the budget divided by this.
	inFlightMarginDivisor = 20

	reasonInFlightResultsCut = "in_flight_results_over_budget"

	// toolOutputOpenPrefix and toolOutputLineEnd find the opening tag
	// toolOutputOpenFormat writes.
	toolOutputOpenPrefix = "<toolOutput "
	toolOutputLineEnd    = "\n"
)

// fitInFlightToolResults is Elelem's post token limit hook. Compaction can
// drop or summarize earlier history, but never the tool results the model is
// about to read, so a round whose parallel calls together return more than
// the context holds would still be sent over the limit. This cuts the largest
// of those results, each to a fair share of the room left, until the request
// fits.
func fitInFlightToolResults(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) error {
	indexes := trailingToolResults(event.Messages)
	if len(indexes) == 0 {
		return nil
	}

	counter := elelem.DefaultTokenCounter()

	for attempt := range inFlightFitAttempts {
		over, err := event.IsOverBudget()
		if err != nil {
			return ctxerrors.Wrap(err, "count request with tool results")
		}

		if !over {
			return nil
		}

		sizes, err := toolResultSizes(counter, event.Messages, indexes)
		if err != nil {
			return err
		}

		overshoot := event.EstimatedTokens - event.BudgetTokens
		margin := attempt * event.BudgetTokens / inFlightMarginDivisor
		room := sumTokens(sizes) - overshoot - margin

		cut, err := cutToolResults(ctx, counter, event.Messages, indexes,
			sizes, fairShares(sizes, room))
		if err != nil {
			return err
		}

		if cut == 0 {
			break
		}

		ctxscope.GetLogger(ctx).Warn("cut tool results to fit the context",
			"reason", reasonInFlightResultsCut,
			"results_cut", cut, "results", len(indexes),
			"estimated_tokens", event.EstimatedTokens,
			"budget_tokens", event.BudgetTokens, "attempt", attempt)
	}

	return nil
}

// trailingToolResults returns the positions of the text-only tool results at
// the end of the transcript, the ones the next model call reads first.
func trailingToolResults(messages []elelem.Message) []int {
	var indexes []int

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != elelem.RoleTool {
			break
		}

		if message.Content.IsTextOnly() {
			indexes = append(indexes, index)
		}
	}

	slices.Reverse(indexes)

	return indexes
}

func toolResultSizes(
	counter elelem.TokenCounter,
	messages []elelem.Message,
	indexes []int,
) ([]int, error) {
	sizes := make([]int, len(indexes))

	for position, index := range indexes {
		tokens, err := workflowContentTokens(counter, messages[index].Text())
		if err != nil {
			return nil, ctxerrors.Wrap(err, "count tool result")
		}

		sizes[position] = tokens
	}

	return sizes, nil
}

// fairShares splits room between the results: one smaller than an even share
// keeps all of it, and what it leaves goes to the larger ones. No share falls
// under inFlightResultFloorTokens.
func fairShares(sizes []int, room int) []int {
	order := make([]int, len(sizes))
	for position := range order {
		order[position] = position
	}

	slices.SortFunc(order, func(left, right int) int {
		return sizes[left] - sizes[right]
	})

	shares := make([]int, len(sizes))
	left := len(order)

	for _, position := range order {
		share := max(room/left, inFlightResultFloorTokens)
		shares[position] = min(sizes[position], share)
		room -= shares[position]
		left--
	}

	return shares
}

func cutToolResults(
	ctx context.Context,
	counter elelem.TokenCounter,
	messages []elelem.Message,
	indexes, sizes, shares []int,
) (int, error) {
	cut := 0

	for position, index := range indexes {
		if sizes[position] <= shares[position] {
			continue
		}

		content, err := cutToolResult(ctx, counter, messages[index].Text(),
			shares[position])
		if err != nil {
			return cut, err
		}

		messages[index].Content = elelem.Text(content)
		cut++
	}

	return cut, nil
}

// cutToolResult shortens one result to about share tokens. A result wrapped
// by markToolOutput keeps its opening tag, closing tag and notice, and only
// the text inside is cut, so the model still reads that the text is data.
func cutToolResult(
	ctx context.Context,
	counter elelem.TokenCounter,
	content string,
	share int,
) (string, error) {
	opening, inner, closing, isWrapped := splitToolOutput(content)
	if !isWrapped {
		cut, err := boundWorkflowToolContent(ctx, content,
			workflowToolOptions{MaxResultTokens: share, Counter: counter})
		if err != nil {
			return "", ctxerrors.Wrap(err, "cut tool result to fit")
		}

		return cut, nil
	}

	overhead, err := workflowContentTokens(counter, opening+closing)
	if err != nil {
		return "", ctxerrors.Wrap(err, "count tool output wrapper")
	}

	innerShare := share - overhead - toolOutputSlackTokens
	if innerShare < 1 {
		return opening + workflowToolTruncatedMarker + closing, nil
	}

	cut, err := boundWorkflowToolContent(ctx, inner,
		workflowToolOptions{MaxResultTokens: innerShare, Counter: counter})
	if err != nil {
		return "", ctxerrors.Wrap(err, "cut wrapped tool result to fit")
	}

	return opening + cut + closing, nil
}

// splitToolOutput takes a markToolOutput block apart. The escaper keeps the
// inner text from holding a closing tag, so the last one ends the block.
func splitToolOutput(content string) (string, string, string, bool) {
	if !strings.HasPrefix(content, toolOutputOpenPrefix) {
		return "", "", "", false
	}

	openingEnd := strings.Index(content, toolOutputLineEnd)
	closingStart := strings.LastIndex(content, toolOutputCloseTag)

	if openingEnd < 0 || closingStart <= openingEnd {
		return "", "", "", false
	}

	openingEnd += len(toolOutputLineEnd)

	return content[:openingEnd], content[openingEnd:closingStart],
		content[closingStart:], true
}

func sumTokens(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}

	return total
}
