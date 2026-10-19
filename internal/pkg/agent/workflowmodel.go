package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

type workflowToolDefinition struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ArgumentsSchema json.RawMessage `json:"argumentsSchema"`
	StrictArguments bool            `json:"strictArguments"`
}

type workflowConversation struct {
	ModelReference   string                   `json:"modelReference"`
	SystemPrompt     string                   `json:"systemPrompt"`
	Messages         []elelem.Message         `json:"messages"`
	Tools            []workflowToolDefinition `json:"tools"`
	MaxContextTokens int                      `json:"maxContextTokens"`
	ReasoningEffort  elelem.ReasoningEffort   `json:"reasoningEffort"`
	Deadline         time.Time                `json:"deadline"`
	Usage            elelem.Usage             `json:"usage"`
	Cost             float64                  `json:"cost"`
	// ReasoningParts holds each round's reasoning as its own string. One
	// growing string would be new text in every saved step, while separate
	// parts are each stored once. Read the whole with reasoning().
	ReasoningParts []string                 `json:"reasoningParts,omitempty"`
	FinishReason   elelem.FinishReason      `json:"finishReason,omitempty"`
	HasToolCalls   bool                     `json:"hasToolCalls,omitempty"`
	Streaming      *bool                    `json:"streaming,omitempty"`
	Runtime        *workflowRuntimeState    `json:"runtime,omitempty"`
	Root           *workflowRootBootstrap   `json:"root,omitempty"`
	Child          *workflowChildBootstrap  `json:"child,omitempty"`
	Stream         *workflowToolStream      `json:"stream,omitempty"`
	InboxIDs       []uuid.UUID              `json:"inboxIds,omitempty"`
	Compaction     *workflowCompactionState `json:"compaction,omitempty"`
	Limits         *workflowExecutionLimits `json:"limits,omitempty"`
}

// reasoning returns the reasoning of every round so far, in order.
func (c workflowConversation) reasoning() string {
	return strings.Join(c.ReasoningParts, "")
}

// A fresh request runs one model round. No automatic tool continuation is set.
func workflowModelRequest(
	models ModelResolver,
	input workflowRoundInput,
) (*elelem.Request, workflowConversation, error) {
	var conversation workflowConversation

	err := json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"decode workflow conversation")
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"validate saved compaction context")
	}

	if err := validateWorkflowChildConversation(conversation,
		input); err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"validate saved child conversation")
	}

	model, err := models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"resolve workflow model")
	}

	model = retainedWorkflowModel(conversation, model)

	tools := elelem.NewToolSet()
	for _, definition := range conversation.Tools {
		tools.Add(elelem.Tool{
			Name: definition.Name, Description: definition.Description,
			ArgumentsSchema: definition.ArgumentsSchema,
			StrictArguments: definition.StrictArguments,
		})
	}

	// These messages belong to the same unfinished execution. Elelem's history
	// builder drops injections from earlier runs, so retain their active input.
	history := append([]elelem.Message(nil), conversation.Messages...)
	for index := range history {
		if history[index].Origin == elelem.MessageOriginInjection {
			history[index].Origin = elelem.MessageOriginSeed
		}
	}

	request := elelem.NewRequest(model.Client).WithModel(model.Model).
		WithPrompt(elelem.NewPrompt().WithSystem(conversation.SystemPrompt).
			WithHistory(history)).
		WithTools(tools).WithMaxContextTokens(conversation.MaxContextTokens).
		WithMaxRounds(workflowManualRequestRoundLimit)
	if conversation.Streaming != nil {
		request.WithStreaming(*conversation.Streaming)
	}

	if conversation.ReasoningEffort != "" {
		request.WithReasoningEffort(conversation.ReasoningEffort)
	}

	return request, conversation, nil
}

func retainedWorkflowModel(
	conversation workflowConversation,
	model ModelClient,
) ModelClient {
	if conversation.Child == nil || conversation.Child.Model == nil {
		return model
	}

	model.Model = *conversation.Child.Model

	return model
}

func workflowRunModelRequest(
	ctx context.Context,
	request *elelem.Request,
	conversation workflowConversation,
) (workflowRoundResult, error) {
	response, err := runWorkflowModelResponse(ctx, request, conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err, "run workflow model")
	}

	return workflowModelResult(response, conversation)
}

func runWorkflowModelResponse(
	ctx context.Context,
	request *elelem.Request,
	conversation workflowConversation,
) (*elelem.Response, error) {
	if !conversation.Deadline.IsZero() {
		var cancel context.CancelFunc

		ctx, cancel = context.WithDeadline(ctx, conversation.Deadline)
		defer cancel()
	}

	response, err := request.Run(ctx)
	if err != nil {
		return response, ctxerrors.Wrap(err, "run workflow model")
	}

	return response, nil
}

func workflowModelResult(
	response *elelem.Response,
	conversation workflowConversation,
) (workflowRoundResult, error) {
	conversation.Messages = directoryHistory(response.Messages)
	conversation.Usage = mergeDirectoryUsage(conversation.Usage, response.Usage)

	conversation.Cost += response.Cost
	if response.Reasoning != "" {
		conversation.ReasoningParts = append(conversation.ReasoningParts,
			response.Reasoning)
	}

	conversation.FinishReason = response.FinishReason
	conversation.HasToolCalls = len(response.ToolCalls) > 0
	conversation.InboxIDs = nil

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"encode workflow model context")
	}

	return workflowRoundResult{
		Context: encoded, Calls: response.ToolCalls, Text: response.Text,
	}, nil
}

func workflowMergeRound(
	encoded json.RawMessage,
	calls []workflowToolInput,
	results []workflowToolResult,
) (json.RawMessage, error) {
	if len(calls) != len(results) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool round result count differs from its calls")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(encoded, &conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode tool round context")
	}

	for index, result := range results {
		if len(result.Context) != 0 {
			var snapshot workflowConversation
			if err := json.Unmarshal(result.Context, &snapshot); err != nil {
				return nil, ctxerrors.Wrap(err, "decode tool runtime state")
			}

			resetWorkflowDirectorySkills(conversation.Runtime,
				calls[index], result)

			runtimeState, err := mergeWorkflowRuntime(
				conversation.Runtime, snapshot.Runtime,
			)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "merge tool runtime state")
			}

			mergeWorkflowDirectoryPrompt(&conversation, snapshot)
			conversation.Runtime = runtimeState
		}

		conversation.Messages = append(conversation.Messages, elelem.Message{
			Role: elelem.RoleTool, ToolCallID: calls[index].Call.ID,
			Content:           elelem.Text(result.Result.Content),
			ToolResultIsError: result.Result.IsError,
			Origin:            elelem.MessageOriginTurn,
		})
	}

	// Providers require every result before any injected message in the round.
	for _, result := range results {
		for _, injection := range result.Injections {
			message := elelem.Message{
				Role: injection.Type, Content: elelem.Text(injection.Content),
				Origin: elelem.MessageOriginInjection, Injection: &injection,
			}
			conversation.Messages = append(conversation.Messages, message)
		}
	}

	output, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode merged tool round context")
	}

	return output, nil
}
