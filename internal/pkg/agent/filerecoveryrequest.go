package agent

import (
	"context"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

func (r *Runtime) recoverWorkflowFiles(
	ctx context.Context,
	request *elelem.Request,
	conversation workflowConversation,
	input workflowRoundInput,
) error {
	recovery, err := r.workflowFileRecovery(ctx, conversation, input)
	if err != nil {
		return ctxerrors.Wrap(err, "load workflow file inventory")
	}

	if len(recovery.reads) == 0 {
		return nil
	}

	if err := r.prepareFileRecoveryRequest(ctx, request, conversation,
		recovery); err != nil {
		return err
	}

	watchFileRecovery(request, recovery, conversation.historyDropped)

	return nil
}

func (r *Runtime) prepareFileRecoveryRequest(
	ctx context.Context,
	request *elelem.Request,
	conversation workflowConversation,
	recovery *fileRecovery,
) error {
	model, err := r.models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve file recovery counter")
	}

	model = retainedWorkflowModel(conversation, model)
	counter := modelTokenCounter(model)
	request.WithTokenCounter(counter)

	budget := conversation.MaxContextTokens
	if budget <= 0 {
		budget = model.Model.ContextSize
	}

	budget -= conversation.OutputReserveTokens

	tools := fileRecoveryTools(conversation.Tools)

	count := func(messages []elelem.Message) (int, error) {
		return counter.Count(messages, tools)
	}
	// Resolve images again from the saved references, without changing the
	// conversation returned to the workflow.
	history, err := resolveImageReferences(ctx, r.store,
		seededWorkflowHistory(conversation.Messages))
	if err != nil {
		return ctxerrors.Wrap(err, "resolve file recovery history")
	}

	messages := elelem.NewPrompt().WithSystem(conversation.SystemPrompt).
		WithHistory(history).Messages()

	messages, err = recovery.recover(ctx, messages, budget, count)
	if err != nil {
		return ctxerrors.Wrap(err, "restore compacted file context")
	}

	request.WithPrompt(elelem.NewPrompt().WithHistory(messages))

	return nil
}

func fileRecoveryTools(definitions []workflowToolDefinition) []elelem.Tool {
	tools := make([]elelem.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, elelem.Tool{
			Name: definition.Name, Description: definition.Description,
			ArgumentsSchema: definition.ArgumentsSchema,
			StrictArguments: definition.StrictArguments,
		})
	}

	return tools
}

func watchFileRecovery(
	request *elelem.Request,
	recovery *fileRecovery,
	moved *bool,
) {
	if moved == nil {
		moved = new(bool)
	}

	request.PreMaxTokensReached(func(ctx context.Context,
		event *elelem.TokenLimitEvent,
	) error {
		// Re-attachments give up before the loss order runs again.
		event.Messages = slices.DeleteFunc(slices.Clone(event.Messages),
			isFileRecoveryMessage)

		return fitWorkflowContext(moved)(ctx, event)
	}).PostMaxTokensReached(func(ctx context.Context,
		event *elelem.TokenLimitEvent,
	) error {
		return recovery.fit(ctx, event, moved)
	})
}

func (r *fileRecovery) fit(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	moved *bool,
) error {
	if err := fitWorkflowInFlightResults(moved)(ctx, event); err != nil {
		return err
	}

	if err := r.reserveNote(ctx, event, moved); err != nil {
		return err
	}

	count := func(messages []elelem.Message) (int, error) {
		event.Messages = messages
		if _, err := event.IsOverBudget(); err != nil {
			return 0, ctxerrors.Wrap(err, "count file recovery request")
		}

		return event.EstimatedTokens, nil
	}

	messages, err := r.recover(ctx, event.Messages, event.BudgetTokens, count)
	if err != nil {
		return err
	}

	event.Messages = messages
	if _, err := event.IsOverBudget(); err != nil {
		return ctxerrors.Wrap(err, "count final file recovery request")
	}

	return nil
}

// Reserve the inventory before the final loss pass. Presence is rebuilt
// after that pass, not inferred from the selected loss operations.
func (r *fileRecovery) reserveNote(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	moved *bool,
) error {
	entries := r.presence(event.Messages)
	if !fileRecoveryLost(entries) {
		return nil
	}

	original := event.Messages
	count := func(messages []elelem.Message) (int, error) {
		event.Messages = messages
		if _, err := event.IsOverBudget(); err != nil {
			return 0, ctxerrors.Wrap(err, "count reserved inventory")
		}

		return event.EstimatedTokens, nil
	}
	_, note, _, err := boundedFileRecoveryNote(entries,
		event.BudgetTokens/fileRecoveryNoteDivisor, count)
	event.Messages = original

	if err != nil {
		return err
	}

	if note.Content == nil {
		return nil
	}

	event.Messages = slices.DeleteFunc(slices.Clone(event.Messages),
		isFileRecoveryMessage)

	position := 0
	if len(event.Messages) > 0 &&
		event.Messages[0].Role == elelem.RoleSystem {
		position = 1
	}

	event.Messages = slices.Insert(event.Messages, position, note)
	if err := fitWorkflowContext(moved)(ctx, event); err != nil {
		return err
	}

	return fitWorkflowInFlightResults(moved)(ctx, event)
}
