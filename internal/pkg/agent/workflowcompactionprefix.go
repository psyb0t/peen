package agent

import (
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

func selectWorkflowCompactionPrefix(
	models ModelResolver,
	input workflowRoundInput,
) (int, error) {
	var conversation workflowConversation

	err := json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "decode compaction prefix context")
	}

	if conversation.Compaction == nil {
		return 0, nil
	}

	request, conversation, err := workflowModelRequest(models, input)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "build compaction budget probe")
	}

	over, err := request.IsTokenLimitReached()
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count original compaction input")
	}

	if !over {
		return 0, nil
	}

	state := conversation.Compaction
	if conversation.MaxContextTokens <= state.MaxOutputTokens {
		return 0, ctxerrors.Wrap(ErrCompactionUnavailable,
			"compaction requires room for the summary allowance")
	}

	budget := conversation.MaxContextTokens - state.MaxOutputTokens
	compactor := &compactor{plan: state.Plan}
	messages := workflowCompactionMessages(conversation)

	return selectWorkflowPrefixCandidate(request, conversation,
		compactor, messages, budget)
}

func selectWorkflowPrefixCandidate(
	request *elelem.Request,
	conversation workflowConversation,
	compactor *compactor,
	messages []elelem.Message,
	budget int,
) (int, error) {
	for units := 1; units <= compactor.plan.coverableUnits(); units++ {
		candidate := compactor.replacePrefix(messages, units,
			compactionSummaryLead)

		history := directoryHistory(candidate)
		for index := range history {
			if history[index].Origin == elelem.MessageOriginInjection {
				history[index].Origin = elelem.MessageOriginSeed
			}
		}

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
