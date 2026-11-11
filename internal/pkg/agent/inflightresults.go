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

	reasonInFlightResultsCut     = "in_flight_results_over_budget"
	reasonInFlightResultsLeftOut = "in_flight_results_left_out"
	reasonReasoningLeftOut       = "reasoning_left_out"

	// inFlightResultLeftOut replaces a result that did not fit even cut short.
	inFlightResultLeftOut = "[Peen left this result out: this round's tool results did not fit the context together. Call the tool again, with fewer calls at once, to read it.]" //nolint:lll // One model-facing line.

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
// fits, retries history removal even for a partial reduction, then stubs
// whole results and clears old reasoning as last fallbacks.
func fitInFlightToolResults(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) error {
	_, err := fitInFlightToolResultsReporting(ctx, event)

	return err
}

// fitInFlightToolResultsReporting reports history removal so a workflow can
// release a compaction plan whose message positions no longer match.
func fitInFlightToolResultsReporting(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) (bool, error) {
	if err := cutInFlightToolResults(ctx, event); err != nil {
		return false, err
	}

	changed, err := leaveOutPartialHistory(ctx, event)
	if err != nil {
		return false, err
	}

	if err := leaveOutInFlightResults(ctx, event,
		trailingToolResults(event.Messages)); err != nil {
		return changed, err
	}

	return changed, leaveOutReasoning(ctx, event)
}

// leaveOutReasoning changes only the outgoing request's message copies.
// Transcript rows are recorded separately, before these limit hooks run.
// Providers may need the live tool round's reasoning to accept its results.
func leaveOutReasoning(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) error {
	protected := liveReasoningIndex(event.Messages)
	cleared := 0
	before := event.EstimatedTokens

	for index, message := range event.Messages {
		if index == protected || message.Role != elelem.RoleAssistant ||
			(message.Reasoning == "" &&
				!elelem.HasProviderReasoning(message.ProviderReasoning)) {
			continue
		}

		over, err := event.IsOverBudget()
		if err != nil {
			return ctxerrors.Wrap(err, "count request before reasoning removal")
		}

		if !over {
			break
		}

		event.Messages[index].Reasoning = ""
		event.Messages[index].ProviderReasoning = nil
		cleared++
	}

	if cleared == 0 {
		return nil
	}

	if _, err := event.IsOverBudget(); err != nil {
		return ctxerrors.Wrap(err, "count request after leaving reasoning out")
	}

	ctxscope.GetLogger(ctx).Warn("left reasoning out to fit the context",
		"reason", reasonReasoningLeftOut, "messages_cleared", cleared,
		"estimated_tokens_before", before,
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens)

	return nil
}

func liveReasoningIndex(messages []elelem.Message) int {
	index := lastAnsweringAssistantIndex(messages)
	if index < 0 || len(messages[index].ToolCalls) == 0 ||
		toolRoundEnd(messages, index) != len(messages) ||
		index+1 == len(messages) {
		return -1
	}

	return index
}

// cutInFlightToolResults keeps every result present so older messages can
// leave before the post hook replaces whole in-flight results with stubs.
func cutInFlightToolResults(
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

// leaveOutInFlightResults replaces whole results with a stub, newest first,
// when cutting each one to its floor still leaves the request over budget.
// That happens when the model asks for more calls at once than the context
// holds even in short. It stubs in batches that double each time, so many
// results cost a few recounts, and the model calls the tool again for any
// result it still needs.
func leaveOutInFlightResults(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	indexes []int,
) error {
	stubbed := 0

	for batch := 1; stubbed < len(indexes); batch *= 2 {
		over, err := event.IsOverBudget()
		if err != nil {
			return ctxerrors.Wrap(err, "count request with cut tool results")
		}

		if !over {
			break
		}

		for range min(batch, len(indexes)-stubbed) {
			stubbed++
			index := indexes[len(indexes)-stubbed]
			event.Messages[index].Content = elelem.Text(inFlightResultLeftOut)
		}
	}

	if stubbed == 0 {
		return nil
	}

	if _, err := event.IsOverBudget(); err != nil {
		return ctxerrors.Wrap(err, "count request with left out tool results")
	}

	ctxscope.GetLogger(ctx).Warn("left tool results out to fit the context",
		"reason", reasonInFlightResultsLeftOut,
		"results_left_out", stubbed, "results", len(indexes),
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens)

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
