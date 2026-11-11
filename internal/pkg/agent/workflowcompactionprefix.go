package agent

import (
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// selectWorkflowCompactionPrefix returns how many completed units to
// summarize and the summary room that leaves, or zero units when the request
// fits. It tries the saved room first and then smaller ones, so a context too
// full for the saved room still gets a tighter summary. It covers the newest
// completed turn only when older history cannot free even the smallest room.
func selectWorkflowCompactionPrefix(
	models ModelResolver,
	input workflowRoundInput,
) (int, int, error) {
	var conversation workflowConversation

	err := json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return 0, 0, ctxerrors.Wrap(err, "decode compaction prefix context")
	}

	if conversation.Compaction == nil {
		return 0, 0, nil
	}

	request, conversation, err := workflowModelRequest(models, input)
	if err != nil {
		return 0, 0, ctxerrors.Wrap(err, "build compaction budget probe")
	}

	over, err := request.IsTokenLimitReached()
	if err != nil {
		return 0, 0, ctxerrors.Wrap(err, "count original compaction input")
	}

	if !over {
		return 0, 0, nil
	}

	compactor := &compactor{plan: conversation.Compaction.Plan}
	messages := workflowCompactionMessages(conversation)

	for _, candidates := range compactor.plan.coverableRanges() {
		units, room, err := selectWorkflowPrefixRoom(request, conversation,
			compactor, messages, candidates)
		if errors.Is(err, ErrCompactionUnavailable) {
			continue
		}

		return units, room, err
	}

	return 0, 0, ctxerrors.Wrap(ErrCompactionUnavailable,
		"completed history cannot free the minimum summary room")
}

// selectWorkflowPrefixRoom tries each summary room, largest first, for the
// fewest units in candidates whose summary brings the request under budget.
func selectWorkflowPrefixRoom(
	request *elelem.Request,
	conversation workflowConversation,
	compactor *compactor,
	messages []elelem.Message,
	candidates unitRange,
) (int, int, error) {
	rooms := compactionSummaryRooms(conversation.Compaction.MaxOutputTokens)
	for _, room := range rooms {
		// The candidate request keeps the reply's reserve, so the summary
		// room has to fit beside it or the probe resolves to no budget.
		if conversation.MaxContextTokens <=
			room+conversation.OutputReserveTokens {
			continue
		}

		units, err := selectWorkflowPrefixCandidate(request, conversation,
			compactor, messages, candidates,
			conversation.MaxContextTokens-room)
		if errors.Is(err, ErrCompactionUnavailable) {
			continue
		}

		if err != nil {
			return 0, 0, err
		}

		return units, room, nil
	}

	return 0, 0, ctxerrors.Wrap(ErrCompactionUnavailable,
		"no summary room fits these units")
}

func selectWorkflowPrefixCandidate(
	request *elelem.Request,
	conversation workflowConversation,
	compactor *compactor,
	messages []elelem.Message,
	candidates unitRange,
	budget int,
) (int, error) {
	for units := candidates.first; units <= candidates.last; units++ {
		candidate := compactor.replacePrefix(messages, units,
			compactionSummaryLead)

		history := seededWorkflowHistory(directoryHistory(candidate))
		request.WithPrompt(elelem.NewPrompt().
			WithSystem(conversation.SystemPrompt).WithHistory(history)).
			WithMaxContextTokens(budget)

		over, err := request.IsTokenLimitReached()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "count compaction prefix candidate")
		}

		if !over {
			return units, nil
		}
	}

	return 0, ctxerrors.Wrap(ErrCompactionUnavailable,
		"completed history cannot free the summary allowance")
}

func workflowCompactionMessages(
	conversation workflowConversation,
) []elelem.Message {
	messages := make([]elelem.Message, 0, len(conversation.Messages)+1)
	if conversation.SystemPrompt != "" {
		messages = append(messages, elelem.Message{
			Role:    elelem.RoleSystem,
			Content: elelem.Text(conversation.SystemPrompt),
		})
	}

	return append(messages, conversation.Messages...)
}
