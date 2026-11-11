package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// maskMinTokens is the smallest old tool result worth replacing with a
	// stub. A smaller one frees less than the stub costs to read.
	maskMinTokens = 256

	// droppedNoticeCallLimit and droppedNoticeArgumentBytes bound the list of
	// left-out tool calls the notice names.
	droppedNoticeCallLimit     = 10
	droppedNoticeArgumentBytes = 120

	// maskedToolResultLead opens the stub that replaces an old tool result.
	// It also marks a result as already masked.
	maskedToolResultLead   = "[Peen left this result out to make room."
	maskedToolResultFormat = maskedToolResultLead + " It held about %d tokens. Call the tool again if you need it.]" //nolint:lll // One model-facing line.

	// droppedHistoryLead opens the notice that names what a request left
	// out. It also marks an earlier notice, which a new one replaces.
	droppedHistoryLead   = "Peen left earlier messages out to fit the context."
	droppedHistoryFormat = droppedHistoryLead + " It left out %d of the model's earlier replies and tool results and %d older messages. The stored transcript still holds them.%s Call a tool again if you need one of its results." //nolint:lll // One model-facing line.
	droppedCallsFormat   = " The tool calls left out were: %s."
	droppedCallSeparator = "; "
	droppedCallsMore     = "and %d more"

	reasonOldToolResultsMasked = "old_tool_results_masked"
	reasonHistoryLeftOut       = "history_left_out"
)

// fitRequestContext is the token limit handler of every model request. It
// frees room in the order that loses the least: old tool results become
// stubs, then the model's own earlier rounds and replies leave when that
// alone makes the request fit, then the results the model is about to read
// are cut, and older messages leave only when nothing else is left. The
// summaries, the requests of the work still in progress, and the turn-end
// notes stay.
func fitRequestContext(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) error {
	_, err := fitRequestContextReporting(ctx, event)

	return err
}

// contextLimitHandler returns the summarizer's handler under summarize mode,
// which falls back to fitRequestContext itself, and fitRequestContext
// otherwise.
func contextLimitHandler(summarizer *compactor) elelem.TokenLimitHandler {
	if summarizer == nil {
		return fitRequestContext
	}

	return summarizer.handle
}

// fitRequestContextReporting runs fitRequestContext and reports whether it
// removed or inserted messages, which moves every later message.
func fitRequestContextReporting(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) (bool, error) {
	isOver := func(messages []elelem.Message) (bool, error) {
		event.Messages = messages

		over, err := event.IsOverBudget()
		if err != nil {
			return false, ctxerrors.Wrap(err, "count request to fit")
		}

		return over, nil
	}

	masked, err := maskOldToolResults(event.Messages,
		liveExchangeStart(event.Messages), isOver)
	if err != nil {
		return false, err
	}

	logMaskedToolResults(ctx, masked)

	changed, err := leaveOutOldHistory(ctx, event, false)
	if err != nil {
		return false, err
	}

	if err := cutInFlightToolResults(ctx, event); err != nil {
		return false, ctxerrors.Wrap(err, "fit in-flight tool results")
	}

	changedAgain, err := leaveOutOldHistory(ctx, event, true)
	if err != nil {
		return false, err
	}

	return changed || changedAgain, nil
}

// fitWorkflowContext is fitRequestContext for a workflow round. It records
// in moved whether the request left out messages, so the round can release a
// summarize plan whose positions no longer match.
func fitWorkflowContext(moved *bool) elelem.TokenLimitHandler {
	return func(ctx context.Context, event *elelem.TokenLimitEvent) error {
		changed, err := fitRequestContextReporting(ctx, event)
		if err != nil {
			return err
		}

		if changed {
			*moved = true
		}

		return nil
	}
}

func fitWorkflowInFlightResults(moved *bool) elelem.TokenLimitHandler {
	return func(ctx context.Context, event *elelem.TokenLimitEvent) error {
		changed, err := fitInFlightToolResultsReporting(ctx, event)
		if err != nil {
			return err
		}

		if changed {
			*moved = true
		}

		return nil
	}
}

// maskOldToolResults replaces old tool results with a stub, oldest first,
// until isOver reports that messages fit. Results from live onward, error
// results, small results, and results already masked stay. It masks in
// batches that double each time, so a long history costs a few recounts.
// It returns how many results it masked.
func maskOldToolResults(
	messages []elelem.Message,
	live int,
	isOver func([]elelem.Message) (bool, error),
) (int, error) {
	counter := elelem.DefaultTokenCounter()

	candidates, err := maskableToolResults(counter, messages[:live])
	if err != nil {
		return 0, err
	}

	masked := 0
	batch := 1

	for masked < len(candidates) {
		over, err := isOver(messages)
		if err != nil {
			return masked, err
		}

		if !over {
			break
		}

		end := min(masked+batch, len(candidates))
		for _, candidate := range candidates[masked:end] {
			call := maskedCallDescription(messages[:candidate.index],
				messages[candidate.index].ToolCallID)
			messages[candidate.index].Content = elelem.Text(
				maskToolResult(messages[candidate.index].Text(),
					candidate.tokens, call))
		}

		masked = end
		batch *= 2
	}

	return masked, nil
}

// maskWorkflowToolResults replaces old tool results in a round's saved
// context with stubs, oldest first, while the round's request is over
// budget. A compaction activity runs it before any summary, so a request
// that masking alone brings under budget needs none. It returns input with
// the masked context, or input as it was when nothing needed masking.
func maskWorkflowToolResults(
	ctx context.Context,
	models ModelResolver,
	input workflowRoundInput,
) (workflowRoundInput, error) {
	request, conversation, err := workflowModelRequest(models, input)
	if err != nil {
		return input, ctxerrors.Wrap(err, "build tool result mask probe")
	}

	isOver := func(messages []elelem.Message) (bool, error) {
		request.WithPrompt(elelem.NewPrompt().
			WithSystem(conversation.SystemPrompt).
			WithHistory(seededWorkflowHistory(directoryHistory(messages))))

		over, err := request.IsTokenLimitReached()
		if err != nil {
			return false, ctxerrors.Wrap(err, "count masked workflow context")
		}

		return over, nil
	}

	masked, err := maskOldToolResults(conversation.Messages,
		liveExchangeStart(conversation.Messages), isOver)
	if err != nil {
		return input, err
	}

	if masked == 0 {
		return input, nil
	}

	logMaskedToolResults(ctx, masked)

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return input, ctxerrors.Wrap(err, "encode masked workflow context")
	}

	input.Execution.Context = encoded

	return input, nil
}

// maskAndSelectCompactionPrefix masks old tool results in a round's saved
// context, then selects the ended turns a summary would replace, as
// selectWorkflowCompactionPrefix does, from the masked context. It returns
// the input carrying that context.
func maskAndSelectCompactionPrefix(
	ctx context.Context,
	models ModelResolver,
	input workflowRoundInput,
) (workflowRoundInput, int, int, error) {
	masked, err := maskWorkflowToolResults(ctx, models, input)
	if err != nil {
		return input, 0, 0, ctxerrors.Wrap(err, "mask old tool results")
	}

	units, room, err := selectWorkflowCompactionPrefix(models, masked)

	return masked, units, room, err
}

type maskableToolResult struct {
	index  int
	tokens int
}

// maskableToolResults lists the tool results worth masking, oldest first.
func maskableToolResults(
	counter elelem.TokenCounter,
	messages []elelem.Message,
) ([]maskableToolResult, error) {
	var candidates []maskableToolResult

	for index, message := range messages {
		if message.Role != elelem.RoleTool || message.ToolResultIsError ||
			!message.Content.IsTextOnly() ||
			strings.Contains(message.Text(), maskedToolResultLead) {
			continue
		}

		tokens, err := workflowContentTokens(counter, message.Text())
		if err != nil {
			return nil, ctxerrors.Wrap(err, "count old tool result")
		}

		if tokens < maskMinTokens {
			continue
		}

		candidates = append(candidates,
			maskableToolResult{index: index, tokens: tokens})
	}

	return candidates, nil
}

// maskToolResult returns the stub for a result of about tokens tokens. A
// result wrapped by markToolOutput keeps its opening tag, closing tag and
// notice, so the model still reads which tool returned it.
func maskToolResult(content string, tokens int, call ...string) string {
	stub := fmt.Sprintf(maskedToolResultFormat, tokens)
	if len(call) != 0 && call[0] != "" {
		stub += " Call: " + call[0]
	}

	opening, _, closing, isWrapped := splitToolOutput(content)
	if !isWrapped {
		return stub
	}

	return opening + stub + closing
}

func logMaskedToolResults(ctx context.Context, masked int) {
	if masked == 0 {
		return
	}

	runtimecontext.Logger(ctx).Warn("masked old tool results to fit the "+
		"context",
		"reason", reasonOldToolResultsMasked, "results_masked", masked)
}

// liveExchangeStart returns where the exchange the model is about to answer
// begins: the latest assistant message with tool calls, when no real user
// message came after it. Without one it returns len(messages).
func liveExchangeStart(messages []elelem.Message) int {
	lastUser := lastRealUserIndex(messages)

	for index := len(messages) - 1; index > lastUser; index-- {
		if len(messages[index].ToolCalls) > 0 {
			return index
		}
	}

	return len(messages)
}

// lastRealUserIndex finds the newest message the user wrote, skipping
// injections and summaries.
func lastRealUserIndex(messages []elelem.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if isRealUserMessage(messages[index]) {
			return index
		}
	}

	return -1
}

func isRealUserMessage(message elelem.Message) bool {
	return message.Role == elelem.RoleUser && !isInjected(message) &&
		!isSummaryMessage(message)
}

// isInjected reports whether Peen injected message. A saved workflow history
// keeps an injection's Injection but seeds its origin, so both count.
func isInjected(message elelem.Message) bool {
	return message.Origin == elelem.MessageOriginInjection ||
		message.Injection != nil
}

func isSummaryMessage(message elelem.Message) bool {
	text := message.Text()

	return strings.HasPrefix(text, compactionSummaryLead) ||
		strings.HasPrefix(text, compactionTurnSummaryLead)
}

func isDroppedNotice(message elelem.Message) bool {
	return message.Role == elelem.RoleSystem &&
		strings.HasPrefix(message.Text(), droppedHistoryLead)
}

// pendingWorkStart returns where the work still in progress begins: after
// the model's last finished reply that came before the newest user message.
// The user's messages and Peen's notes from there on are what the model is
// still acting on.
func pendingWorkStart(messages []elelem.Message) int {
	lastUser := lastRealUserIndex(messages)

	for index := lastUser - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == elelem.RoleAssistant &&
			len(message.ToolCalls) == 0 && !isInjected(message) {
			return index + 1
		}
	}

	return 0
}

// historyRange is messages[start:end], left out as one piece.
type historyRange struct {
	start int
	end   int
}

// droppableHistory lists what leaveOutOldHistory may leave out, in the order
// it leaves it out: the model's tool rounds, then its text replies, then,
// with withOlder, older messages before the work in progress. The system
// prompt, summaries, the live exchange, injections the model has not
// answered yet, and earlier notices are not listed.
func droppableHistory(
	messages []elelem.Message,
	withOlder bool,
) []historyRange {
	live := liveExchangeStart(messages)
	rounds, replies := modelHistory(messages, live)

	if !withOlder {
		return slices.Concat(rounds, replies)
	}

	return slices.Concat(rounds, replies, olderHistory(messages, live))
}

// modelHistory lists the model's own messages before live: its tool rounds,
// each with its results, and its text replies.
func modelHistory(
	messages []elelem.Message,
	live int,
) ([]historyRange, []historyRange) {
	var rounds, replies []historyRange

	for index := range live {
		message := messages[index]
		if message.Role != elelem.RoleAssistant || isInjected(message) {
			continue
		}

		piece := historyRange{start: index, end: toolRoundEnd(messages, index)}
		if len(message.ToolCalls) > 0 {
			rounds = append(rounds, piece)

			continue
		}

		replies = append(replies, piece)
	}

	return rounds, replies
}

// olderHistory lists the user and system messages before the work in
// progress that may leave, one by one, oldest first.
func olderHistory(messages []elelem.Message, live int) []historyRange {
	end := min(pendingWorkStart(messages), live)
	lastAnswer := lastAnsweringAssistantIndex(messages)

	var older []historyRange

	for index := range end {
		if isKeptOlderMessage(messages[index], index, lastAnswer) {
			continue
		}

		older = append(older, historyRange{start: index, end: index + 1})
	}

	return older
}

// isKeptOlderMessage reports whether an older message stays: the model's
// own messages, which modelHistory handles, the system prompt, summaries,
// notices, and injections the model has not answered yet.
func isKeptOlderMessage(
	message elelem.Message,
	index, lastAnswer int,
) bool {
	isModelMessage := message.Role == elelem.RoleAssistant ||
		message.Role == elelem.RoleTool
	isSystemPrompt := index == 0 && message.Role == elelem.RoleSystem
	isUnanswered := isInjected(message) && index > lastAnswer

	return isModelMessage || isSystemPrompt || isUnanswered ||
		isSummaryMessage(message) || isDroppedNotice(message)
}

// lastAnsweringAssistantIndex finds the newest assistant message the model
// wrote, skipping injections.
func lastAnsweringAssistantIndex(messages []elelem.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == elelem.RoleAssistant &&
			!isInjected(messages[index]) {
			return index
		}
	}

	return -1
}

// toolRoundEnd returns the end of the message at index together with the
// tool results that answer it.
func toolRoundEnd(messages []elelem.Message, index int) int {
	end := index + 1
	if len(messages[index].ToolCalls) == 0 {
		return end
	}

	for end < len(messages) && messages[end].Role == elelem.RoleTool {
		end++
	}

	return end
}

// leaveOutOldHistory removes droppable history, in droppableHistory's
// order, until the request fits, and puts a notice naming what it removed
// where the first removed message was. withOlder lets it remove older
// messages too. It removes nothing when even all of it would not fit,
// because history lost for nothing helps no one, and the provider then
// decides whether the request fits. The post hook retries with partial
// removal before its last fallbacks. It reports whether it changed the
// messages.
func leaveOutOldHistory(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	withOlder bool,
) (bool, error) {
	over, err := event.IsOverBudget()
	if err != nil {
		return false, ctxerrors.Wrap(err, "count request before leaving out")
	}

	if !over {
		return false, nil
	}

	original := event.Messages
	candidates := droppableHistory(original, withOlder)
	counter := elelem.DefaultTokenCounter()
	chosen := 0

	for {
		more, err := chooseDroppedHistory(counter, original,
			candidates[chosen:], event.EstimatedTokens-event.BudgetTokens)
		if err != nil {
			return false, err
		}

		next := min(chosen+max(more, 1), len(candidates))
		if next == chosen {
			event.Messages = original
			if _, err := event.IsOverBudget(); err != nil {
				return false, ctxerrors.Wrap(err, "recount restored request")
			}

			if withOlder {
				logHistoryUnfit(ctx, event, len(candidates))
			}

			return false, nil
		}

		chosen = next
		event.Messages = withoutHistory(original, candidates[:chosen])

		over, err := event.IsOverBudget()
		if err != nil {
			return false, ctxerrors.Wrap(err, "count request after leaving out")
		}

		if !over {
			break
		}
	}

	logHistoryLeftOut(ctx, event, len(original))

	return true, nil
}

// leaveOutPartialHistory is the post hook's last history attempt. Unlike the
// ladder it keeps an incomplete reduction, but only if the notice and kept
// messages together cost less than the original request.
func leaveOutPartialHistory(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) (bool, error) {
	over, err := event.IsOverBudget()
	if err != nil {
		return false, ctxerrors.Wrap(err,
			"count request before partial removal")
	}

	if !over {
		return false, nil
	}

	original := event.Messages
	before := event.EstimatedTokens
	candidates := droppableHistory(original, true)
	counter := elelem.DefaultTokenCounter()
	chosen := 0

	for chosen < len(candidates) && over {
		more, err := chooseDroppedHistory(counter, original,
			candidates[chosen:], event.EstimatedTokens-event.BudgetTokens)
		if err != nil {
			return false, err
		}

		chosen = min(chosen+max(more, 1), len(candidates))
		event.Messages = withoutHistory(original, candidates[:chosen])

		over, err = event.IsOverBudget()
		if err != nil {
			return false, ctxerrors.Wrap(err,
				"count request after partial removal")
		}
	}

	if event.EstimatedTokens >= before {
		event.Messages = original
		if _, err := event.IsOverBudget(); err != nil {
			return false, ctxerrors.Wrap(err, "recount restored history")
		}

		return false, nil
	}

	logHistoryLeftOut(ctx, event, len(original))

	return true, nil
}

// chooseDroppedHistory counts how many of candidates, taken in order, have
// an estimated size that covers need tokens. When all of them fall short it
// returns one more than there are, which the caller caps.
func chooseDroppedHistory(
	counter elelem.TokenCounter,
	messages []elelem.Message,
	candidates []historyRange,
	need int,
) (int, error) {
	freed := 0

	for count, candidate := range candidates {
		if freed >= need {
			return count, nil
		}

		tokens, err := counter.Count(messages[candidate.start:candidate.end],
			nil)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "count droppable history")
		}

		freed += tokens
	}

	if freed >= need {
		return len(candidates), nil
	}

	return len(candidates) + 1, nil
}

// withoutHistory returns messages without the chosen ranges and without any
// earlier notice, with one notice naming what is left out where the first
// removed message was.
func withoutHistory(
	messages []elelem.Message,
	chosen []historyRange,
) []elelem.Message {
	if len(chosen) == 0 {
		return messages
	}

	removed := removedPositions(len(messages), chosen)
	left := leftOutHistory{first: -1}
	kept := make([]elelem.Message, 0, len(messages))

	for index, message := range messages {
		isRemoved := removed[index] || isDroppedNotice(message)
		if !isRemoved {
			kept = append(kept, message)

			continue
		}

		left.add(message, len(kept), removed[index])
	}

	if left.first < 0 {
		return kept
	}

	notice := elelem.Message{
		Role: elelem.RoleSystem,
		Content: elelem.Text(droppedHistoryNotice(left.rounds, left.older,
			left.calls)),
		Origin: elelem.MessageOriginTurn,
	}

	return slices.Insert(kept, left.first, notice)
}

func removedPositions(count int, chosen []historyRange) []bool {
	removed := make([]bool, count)

	for _, piece := range chosen {
		for index := piece.start; index < piece.end; index++ {
			removed[index] = true
		}
	}

	return removed
}

// leftOutHistory tallies what withoutHistory removes, for the notice.
type leftOutHistory struct {
	first  int
	rounds int
	older  int
	calls  []string
}

// add records one removed message at position in the kept messages. An
// earlier notice only moves where the new one goes.
func (l *leftOutHistory) add(
	message elelem.Message,
	position int,
	isChosen bool,
) {
	if l.first < 0 {
		l.first = position
	}

	if !isChosen {
		return
	}

	switch message.Role {
	case elelem.RoleAssistant:
		l.rounds++
		l.calls = append(l.calls, describeToolCalls(message.ToolCalls)...)
	case elelem.RoleTool:
	default:
		l.older++
	}
}

// describeToolCalls names each call with its arguments, shortened.
func describeToolCalls(calls []elelem.ToolCall) []string {
	described := make([]string, 0, len(calls))

	for _, call := range calls {
		arguments := string(call.Arguments)
		if !json.Valid(call.Arguments) {
			arguments = ""
		}

		if len(arguments) > droppedNoticeArgumentBytes {
			arguments = workflowUTF8Prefix(arguments,
				droppedNoticeArgumentBytes) + workflowToolTruncatedMarker
		}

		described = append(described,
			strings.TrimSpace(call.Name+" "+arguments))
	}

	return described
}

func droppedHistoryNotice(rounds, older int, calls []string) string {
	listed := ""

	if len(calls) > 0 {
		shown := calls
		if len(shown) > droppedNoticeCallLimit {
			shown = append(slices.Clone(calls[:droppedNoticeCallLimit]),
				fmt.Sprintf(droppedCallsMore,
					len(calls)-droppedNoticeCallLimit))
		}

		listed = fmt.Sprintf(droppedCallsFormat,
			strings.Join(shown, droppedCallSeparator))
	}

	return fmt.Sprintf(droppedHistoryFormat, rounds, older, listed)
}

func logHistoryLeftOut(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	before int,
) {
	runtimecontext.Logger(ctx).Warn("left history out of the request to fit "+
		"the context",
		"reason", reasonHistoryLeftOut,
		"messages_before", before,
		"messages_after", len(event.Messages),
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens,
	)
}

func logHistoryUnfit(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	candidates int,
) {
	runtimecontext.Logger(ctx).Warn("the request does not fit even without "+
		"its droppable history, sending it whole",
		"reason", reasonHistoryLeftOut,
		"droppable", candidates,
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens,
	)
}
