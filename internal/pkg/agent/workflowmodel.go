package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

const (
	// maxOutputLimitContinuations bounds the extra rounds one execution gets
	// for replies cut off at the output limit.
	maxOutputLimitContinuations = 3

	outputLimitNoteFormat = "Your last reply stopped at the output limit " +
		"of %d tokens, reasoning included, before you finished. Continue " +
		"from where it stopped. Reason more briefly, and split long output, " +
		"such as a large file, across several smaller tool calls."
)

type workflowToolDefinition struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ArgumentsSchema json.RawMessage `json:"argumentsSchema"`
	StrictArguments bool            `json:"strictArguments"`
}

type workflowConversation struct {
	ToolStarts map[string]time.Time `json:"toolStarts,omitempty"`

	Repeats          map[string]workflowRepeatCount `json:"repeats,omitempty"`
	ModelReference   string                         `json:"modelReference"`
	SystemPrompt     string                         `json:"systemPrompt"`
	Messages         []elelem.Message               `json:"messages"`
	Tools            []workflowToolDefinition       `json:"tools"`
	MaxContextTokens int                            `json:"maxContextTokens"`
	// OutputReserveTokens is saved with the execution, so a resumed one keeps
	// the budget it started with. Executions saved before it existed read 0,
	// which keeps no reserve, as they did.
	OutputReserveTokens int                    `json:"outputReserveTokens"`
	ReasoningEffort     elelem.ReasoningEffort `json:"reasoningEffort"`
	Usage               elelem.Usage           `json:"usage"`
	Cost                float64                `json:"cost"`
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

	// CutOffRounds counts the rounds this execution added because a reply
	// stopped at the output limit. See continueCutOffReply.
	CutOffRounds int `json:"cutOffRounds,omitempty"`

	// historyDropped is set when one model request left out old history to
	// fit the budget. It lives for that request only and is never saved.
	historyDropped *bool
}

// reasoning returns the reasoning of every round so far, in order.
func (c workflowConversation) reasoning() string {
	return strings.Join(c.ReasoningParts, "")
}

// A fresh request runs one model round. No automatic tool continuation is set.
// historyResolver prepares a saved history for a provider request.
type historyResolver func(history []elelem.Message) ([]elelem.Message, error)

// workflowModelRequest builds the request a saved context describes, with
// image parts still naming their content blobs. It is for counting and
// fitting the context, not for sending it.
func workflowModelRequest(
	models ModelResolver,
	input workflowRoundInput,
) (*elelem.Request, workflowConversation, error) {
	return workflowModelRequestWith(models, input, nil)
}

// workflowSendRequest builds the request a model round sends, with every
// image the history names read back from content_blobs. The returned
// conversation keeps the references.
func workflowSendRequest(
	ctx context.Context,
	models ModelResolver,
	images imageBlobReader,
	input workflowRoundInput,
) (*elelem.Request, workflowConversation, error) {
	request, conversation, err := workflowModelRequestWith(models, input,
		func(history []elelem.Message) ([]elelem.Message, error) {
			return resolveImageReferences(ctx, images, history)
		})
	if err != nil {
		return request, conversation, err
	}

	if err := checkTurnTokenLimit(conversation); err != nil {
		return nil, conversation, err
	}

	return request, conversation, nil
}

// checkTurnTokenLimit refuses another model round once the rounds so far
// have billed the run's token limit. The check runs before each round, so
// the round that crosses the limit still finishes.
func checkTurnTokenLimit(conversation workflowConversation) error {
	return turnTokenLimitFor(conversation.Usage, conversation.Limits,
		conversation.Child != nil)
}

// decodeWorkflowConversation reads and checks a round's saved context.
func decodeWorkflowConversation(
	input workflowRoundInput,
) (workflowConversation, error) {
	var conversation workflowConversation

	err := json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return conversation, ctxerrors.Wrap(err,
			"decode workflow conversation")
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return conversation, ctxerrors.Wrap(err,
			"validate saved compaction context")
	}

	if err := validateWorkflowChildConversation(conversation,
		input); err != nil {
		return conversation, ctxerrors.Wrap(err,
			"validate saved child conversation")
	}

	return conversation, nil
}

func workflowModelRequestWith(
	models ModelResolver,
	input workflowRoundInput,
	resolve historyResolver,
) (*elelem.Request, workflowConversation, error) {
	conversation, err := decodeWorkflowConversation(input)
	if err != nil {
		return nil, conversation, err
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

	history := seededWorkflowHistory(conversation.Messages)
	if resolve != nil {
		history, err = resolve(history)
		if err != nil {
			return nil, conversation, ctxerrors.Wrap(err,
				"read workflow history images")
		}
	}

	request := elelem.NewRequest(model.Client).WithModel(model.Model).
		WithPrompt(elelem.NewPrompt().WithSystem(conversation.SystemPrompt).
			WithHistory(history)).
		WithTools(tools).WithMaxContextTokens(conversation.MaxContextTokens).
		WithOutputReserveTokens(conversation.OutputReserveTokens).
		WithMaxRounds(workflowManualRequestRoundLimit)
	withOutputLimit(request, conversation.OutputReserveTokens)

	if conversation.Streaming != nil {
		request.WithStreaming(*conversation.Streaming)
	}

	watchDroppedHistory(request, &conversation)

	// The saved level is what the client asked for. Elelem refuses a level
	// outside the model's range, so it is fitted as the turn path does.
	effort, _ := fitReasoningEffort(conversation.ReasoningEffort, model.Model,
		model.Client.Capabilities(model.Model))
	if effort != "" {
		request.WithReasoningEffort(effort)
	}

	return request, conversation, nil
}

// watchDroppedHistory records message removal in either context-fit hook,
// so the round releases a summarize plan whose positions no longer match.
func watchDroppedHistory(
	request *elelem.Request,
	conversation *workflowConversation,
) {
	conversation.historyDropped = new(bool)
	request.PreMaxTokensReached(
		fitWorkflowContext(conversation.historyDropped)).
		PostMaxTokensReached(
			fitWorkflowInFlightResults(conversation.historyDropped))
}

// seededWorkflowHistory copies saved messages for a new request. They belong
// to the same unfinished execution, and Elelem's history builder drops
// injections from earlier runs, so their active input is kept as seed history.
func seededWorkflowHistory(messages []elelem.Message) []elelem.Message {
	history := append([]elelem.Message(nil), messages...)
	for index := range history {
		if history[index].Origin == elelem.MessageOriginInjection {
			history[index].Origin = elelem.MessageOriginSeed
		}
	}

	return history
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
	response, err := runWorkflowModelResponse(ctx, request)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err, "run workflow model")
	}

	return workflowModelResult(ctx, response, conversation)
}

func runWorkflowModelResponse(
	ctx context.Context,
	request *elelem.Request,
) (*elelem.Response, error) {
	response, err := request.Run(ctx)
	if err != nil {
		return response, ctxerrors.Wrap(err, "run workflow model")
	}

	return response, nil
}

func workflowModelResult(
	ctx context.Context,
	response *elelem.Response,
	conversation workflowConversation,
) (workflowRoundResult, error) {
	conversation.Messages = savedHistory(response.Messages)
	releaseDroppedCompactionPlan(ctx, &conversation)

	conversation.Usage = workflowSavedUsage(
		mergeDirectoryUsage(conversation.Usage, response.Usage),
	)

	conversation.Cost += response.Cost
	if response.Reasoning != "" {
		conversation.ReasoningParts = append(conversation.ReasoningParts,
			response.Reasoning)
	}

	conversation.FinishReason = response.FinishReason
	conversation.HasToolCalls = len(response.ToolCalls) > 0
	conversation.InboxIDs = nil
	continued := continueCutOffReply(&conversation, response)

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"encode workflow model context")
	}

	return workflowRoundResult{
		Context: encoded, Calls: validToolCalls(response.ToolCalls),
		Text:         response.Text,
		FinishReason: response.FinishReason, Continue: continued,
	}, nil
}

// continueCutOffReply gives the model another round when its reply stopped
// at the output limit without a tool call. Ended there, the turn would finish
// on a half-written answer, often one cut off while still reasoning. A system
// note says the reply was cut and asks for a shorter continuation. After
// maxOutputLimitContinuations such rounds the turn ends as it is, so a model
// that keeps hitting the limit cannot loop.
func continueCutOffReply(
	conversation *workflowConversation,
	response *elelem.Response,
) bool {
	if response.FinishReason != elelem.FinishReasonLength ||
		len(response.ToolCalls) > 0 ||
		conversation.CutOffRounds >= maxOutputLimitContinuations {
		return false
	}

	conversation.CutOffRounds++
	conversation.Messages = append(conversation.Messages, elelem.Message{
		Role: elelem.RoleSystem,
		Content: elelem.Text(fmt.Sprintf(outputLimitNoteFormat,
			conversation.OutputReserveTokens)),
		Origin: elelem.MessageOriginTurn,
	})

	return true
}

// workflowSavedUsage returns usage without the error of each failed provider
// attempt. The conversation is saved as JSON, where an error encodes as an
// object that cannot be decoded back, so a call that succeeded after a retry
// would make the next round fail. The model call record keeps each attempt's
// error text.
func workflowSavedUsage(usage elelem.Usage) elelem.Usage {
	if len(usage.Retry.FailedAttempts) == 0 {
		return usage
	}

	attempts := make([]elelem.RetryAttempt, len(usage.Retry.FailedAttempts))
	for index, attempt := range usage.Retry.FailedAttempts {
		attempt.Err = nil
		attempts[index] = attempt
	}

	usage.Retry.FailedAttempts = attempts

	return usage
}

// workflowRoundInjections are the messages a tool round adds after its
// results: each result's injections, then the image it read, if any.
func workflowRoundInjections(
	results []workflowToolResult,
) ([]elelem.Message, error) {
	var messages []elelem.Message

	for _, result := range results {
		for _, injection := range result.Injections {
			messages = append(messages, elelem.Message{
				Role: injection.Type, Content: elelem.Text(injection.Content),
				Origin: elelem.MessageOriginInjection, Injection: &injection,
			})
		}

		images, err := resultImages(result.Result)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "attach tool round images")
		}

		if len(images) > 0 {
			messages = append(messages, toolImageMessage(images))
		}
	}

	return messages, nil
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
		retainWorkflowRepeat(&conversation, result.Repeat)

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
	messages, err := workflowRoundInjections(results)
	if err != nil {
		return nil, err
	}

	conversation.Messages = append(conversation.Messages, messages...)

	output, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode merged tool round context")
	}

	return output, nil
}
