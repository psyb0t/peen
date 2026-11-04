package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// compactionTurnSummaryLead opens the summary of the earlier rounds of
	// the turn in progress. It also marks the message, so a later pass rolls
	// it into the next summary instead of keeping it raw.
	compactionTurnSummaryLead      = "Summary of the earlier work in this turn, replacing the tool rounds it covers:\n" //nolint:lll // One model-facing line.
	compactionReasonTurnSummarized = "turn_rounds_summarized"
)

// workflowTurnCut is the stretch of the turn in progress a summary replaces:
// messages[start:end], with room tokens kept free for the summary.
type workflowTurnCut struct {
	start int
	end   int
	room  int
}

// compactWorkflowCurrentTurn summarizes the oldest rounds of the turn in
// progress when no ended turn can free the room. One long turn would
// otherwise lose its oldest rounds without a trace once it outgrows the
// context. The turn's opening user messages and its newest round stay raw,
// a summary from an earlier pass is folded into the next one, and the stored
// transcript keeps every message. It returns the context unchanged when the
// request fits or no cut fits.
func (r *Runtime) compactWorkflowCurrentTurn(
	ctx context.Context,
	built *compactor,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	unchanged := workflowCompactionResult{Context: input.Execution.Context}

	request, conversation, err := workflowModelRequest(r.models, input)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"build turn compaction budget probe")
	}

	if conversation.Compaction == nil {
		return unchanged, nil
	}

	over, err := request.IsTokenLimitReached()
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"count turn compaction input")
	}

	if !over {
		return unchanged, nil
	}

	messages := workflowCompactionMessages(conversation)

	cut, found, err := selectWorkflowTurnCut(request, conversation, messages)
	if err != nil {
		return workflowCompactionResult{}, err
	}

	if !found {
		logNothingToSummarize(ctx, ctxerrors.Wrap(ErrCompactionUnavailable,
			"no rounds of the current turn free the minimum summary room"))

		return unchanged, nil
	}

	return r.summarizeWorkflowTurnCut(ctx, built, input, conversation,
		messages, cut)
}

// summarizeWorkflowTurnCut replaces the rounds cut covers with their summary
// and checks that the result fits the request budget.
func (r *Runtime) summarizeWorkflowTurnCut(
	ctx context.Context,
	built *compactor,
	input workflowRoundInput,
	conversation workflowConversation,
	messages []elelem.Message,
	cut workflowTurnCut,
) (workflowCompactionResult, error) {
	built.room = cut.room

	summary, err := built.summarize(ctx,
		renderCompactionMessages(messages[cut.start:cut.end]))
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"summarize earlier rounds of the turn")
	}

	text := strings.TrimSpace(summary.Text)
	if text == "" {
		return workflowCompactionResult{}, ctxerrors.Wrap(
			ErrCompactionEmptySummary, "summarizer returned no turn summary")
	}

	conversation.Messages = savedHistory(spliceTurnSummary(messages, cut,
		compactionTurnSummaryLead+text))
	if err := r.checkCompactedWorkflowBudget(conversation, input); err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"recount turn compaction result")
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"encode turn compaction result")
	}

	runtimecontext.Logger(ctx).Info(
		"summarized earlier rounds of the current turn",
		"reason", compactionReasonTurnSummarized,
		"messages_covered", cut.end-cut.start,
		"summary_tokens", summary.OutputTokens,
		"summary_room_tokens", cut.room,
	)

	return workflowCompactionResult{Context: encoded}, nil
}

// selectWorkflowTurnCut finds the fewest leading rounds of the turn in
// progress whose summary brings the request under budget. It tries the saved
// summary room first and then smaller ones.
func selectWorkflowTurnCut(
	request *elelem.Request,
	conversation workflowConversation,
	messages []elelem.Message,
) (workflowTurnCut, bool, error) {
	state := conversation.Compaction
	plan := state.Plan

	start := turnRoundsStart(messages,
		plan.LeadCount+plan.messageCount(len(plan.Units)))
	ends := turnRoundEnds(messages, start)

	for _, room := range compactionSummaryRooms(state.MaxOutputTokens) {
		if conversation.MaxContextTokens <=
			room+conversation.OutputReserveTokens {
			continue
		}

		budget := conversation.MaxContextTokens - room

		for _, end := range ends {
			cut := workflowTurnCut{start: start, end: end, room: room}
			candidate := spliceTurnSummary(messages, cut,
				compactionTurnSummaryLead)

			request.WithPrompt(elelem.NewPrompt().
				WithSystem(conversation.SystemPrompt).
				WithHistory(seededWorkflowHistory(
					directoryHistory(candidate)))).
				WithMaxContextTokens(budget)

			over, err := request.IsTokenLimitReached()
			if err != nil {
				return workflowTurnCut{}, false, ctxerrors.Wrap(err,
					"count turn compaction candidate")
			}

			if !over {
				return cut, true, nil
			}
		}
	}

	return workflowTurnCut{}, false, nil
}

// turnRoundsStart returns where the summarizable rounds of the turn begin:
// past the turn's opening user messages, which stay raw. A summary from an
// earlier pass is not skipped, so it is folded into the next one.
func turnRoundsStart(messages []elelem.Message, turnStart int) int {
	start := min(turnStart, len(messages))
	for start < len(messages) && messages[start].Role == elelem.RoleUser &&
		!isTurnSummary(messages[start]) {
		start++
	}

	return start
}

// turnRoundEnds lists every place a summary of the rounds after start may
// end, oldest first. Each ends before a message that is not a tool result,
// so a call and its results are never split. The newest assistant message
// and everything after it stay raw.
func turnRoundEnds(messages []elelem.Message, start int) []int {
	newest := start
	for index := len(messages) - 1; index > start; index-- {
		if messages[index].Role == elelem.RoleAssistant {
			newest = index

			break
		}
	}

	ends := make([]int, 0, newest-start)

	for index := start + 1; index <= newest; index++ {
		if messages[index].Role != elelem.RoleTool {
			ends = append(ends, index)
		}
	}

	return ends
}

func isTurnSummary(message elelem.Message) bool {
	return strings.HasPrefix(message.Text(), compactionTurnSummaryLead)
}

// spliceTurnSummary replaces messages[cut.start:cut.end] with one summary
// message holding content.
func spliceTurnSummary(
	messages []elelem.Message,
	cut workflowTurnCut,
	content string,
) []elelem.Message {
	next := make([]elelem.Message, 0, len(messages)-(cut.end-cut.start)+1)
	next = append(next, messages[:cut.start]...)
	next = append(next, compactionSummaryMessage(content))

	return append(next, messages[cut.end:]...)
}

// renderCompactionMessages builds a summarizer input from raw messages, in
// the same transcript format a summary of ended turns uses.
func renderCompactionMessages(messages []elelem.Message) string {
	builder := &strings.Builder{}
	builder.WriteString(compactionTranscriptOpenTag)
	builder.WriteString(compactionLineBreak)

	for _, message := range messages {
		writeCompactionMessage(builder, message)
	}

	builder.WriteString(compactionTranscriptCloseTag)

	return builder.String()
}
