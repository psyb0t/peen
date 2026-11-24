package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	structuredOutputActivityName = "structuredOutputV1"
	outputSchemaRetryFormat      = `<note kind="outputSchemaInvalid">%s</note>`
	outputSchemaErrorBytes       = 2048
	outputSchemaName             = "output"
)

type workflowStructuredInput struct {
	Round  workflowRoundInput  `json:"round"`
	Result workflowRoundResult `json:"result"`
}

func (e *workflowExecution) structuredOutput(
	ctx context.Context,
	input workflowStructuredInput,
) (workflowActivityResult[workflowRoundResult], error) {
	return runWorkflowCheckpoint(ctx, e, input.Round.Execution,
		session.ScheduleActivityInput{
			StepID:       fmt.Sprintf("structuredOutput:%d", input.Round.Round),
			Kind:         models.ActivityKindModel,
			Name:         structuredOutputActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (workflowRoundResult, error) {
			if e.options.StructuredOutput == nil {
				return input.Result, ctxerrors.Wrap(commerr.ErrInvalidState,
					"structured output callback is unavailable")
			}

			return e.options.StructuredOutput(ctx, input)
		}, func(result workflowRoundResult) json.RawMessage {
			return result.Context
		})
}

func (c *rootWorkflowCallbacks) structuredOutput(
	ctx context.Context,
	input workflowStructuredInput,
) (workflowRoundResult, error) {
	shared, release, err := c.acquire(ctx, input.Round.Execution, true)
	if err != nil {
		return input.Result, ctxerrors.Wrap(err, "acquire output callback")
	}
	defer release()

	return c.runtime.runStructuredOutput(ctx, shared.prepared, input)
}

func (r *Runtime) runStructuredOutput(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowStructuredInput,
) (workflowRoundResult, error) {
	conversation, err := decodeWorkflowConversation(input.Round)
	if err != nil {
		return input.Result, err
	}

	if conversation.Root == nil {
		return input.Result, ctxerrors.Wrap(commerr.ErrInvalidState,
			"structured output requires a root turn")
	}

	schema := conversation.Root.OutputSchema

	validator, err := compileOutputSchema(schema, r.maxOutputSchemaBytes)
	if err != nil {
		return input.Result, err
	}

	for attempt := 0; attempt < r.outputSchemaAttempts; attempt++ {
		if limitErr := turnTokenLimitFor(conversation.Usage,
			conversation.Limits, false); limitErr != nil {
			input.Result.OutputError = publicerror.Message(limitErr)

			break
		}

		response, callErr := r.structuredOutputAttempt(ctx, prepared,
			input.Round, conversation, schema, input.Result.OutputError)
		if callErr != nil {
			return input.Result, ctxerrors.Wrap(callErr,
				"run structured output attempt")
		}

		conversation.Usage = workflowSavedUsage(mergeDirectoryUsage(
			conversation.Usage, response.Usage))
		conversation.Cost += response.Cost

		output, validateErr := validateStructuredAnswer(
			response.Text, validator)
		if validateErr == nil {
			input.Result.Output = output
			input.Result.OutputError = ""

			break
		}

		input.Result.OutputError = events.TruncateToRuneBoundary(
			publicerror.Text(validateErr.Error()), outputSchemaErrorBytes)
	}

	input.Result.Context, err = json.Marshal(conversation)
	if err != nil {
		return input.Result, ctxerrors.Wrap(err, "save structured output usage")
	}

	return input.Result, nil
}

func (r *Runtime) structuredOutputAttempt(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
	conversation workflowConversation,
	schema json.RawMessage,
	validationError string,
) (*elelem.Response, error) {
	encoded, err := structuredOutputRequestContext(conversation,
		validationError)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode final output request context")
	}

	input.Execution.Context = encoded

	request, _, err := workflowSendRequest(ctx, r.models, r.store, input)
	if err != nil {
		return nil, err
	}

	request.WithTools(nil).
		WithStreaming(false).
		WithTimeout(r.modelRequestTimeout)

	if prepared.model.Client.Capabilities(prepared.model.Model).
		SupportsResponseFormatJSONSchema {
		request.WithJSONSchema(outputSchemaName, schema, false)
	}

	audit, err := r.rootWorkflowModelAudit(ctx, prepared, input, conversation)
	if err != nil {
		return nil, err
	}

	request.OnRoundStart(audit.onRoundStart).OnRoundEnd(audit.onRoundEnd)

	startedAt := time.Now()

	response, err := request.Run(ctx)

	finishErr := audit.finish(context.WithoutCancel(ctx), response, err)
	observeModelRequest(ctx, r.metrics, modelMetricStageTurn,
		modelMetricFunctionRunProvider, conversation.ModelReference,
		startedAt, time.Time{}, response, errors.Join(err, finishErr))

	if err != nil {
		return nil, ctxerrors.Wrap(err, "request structured output")
	}

	if finishErr != nil {
		return nil, ctxerrors.Wrap(finishErr, "finish structured output audit")
	}

	return response, nil
}

func structuredOutputRequestContext(
	conversation workflowConversation,
	validationError string,
) (json.RawMessage, error) {
	conversation.Tools = nil

	conversation.Messages = slices.Clone(conversation.Messages)
	if validationError != "" {
		conversation.Messages = append(conversation.Messages, elelem.Message{
			Role: elelem.RoleUser,
			Content: elelem.Text(fmt.Sprintf(outputSchemaRetryFormat,
				escapeElement(validationError))),
		})
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode structured output request")
	}

	return encoded, nil
}

func validateStructuredAnswer(
	text string,
	schema *jsonschema.Schema,
) (json.RawMessage, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		if _, body, found := strings.Cut(text, "\n"); found {
			text = strings.TrimSpace(strings.TrimSuffix(body, "```"))
		}
	}

	if !json.Valid([]byte(text)) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"final answer must be one JSON value")
	}

	var value any

	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()

	if err := decoder.Decode(&value); err != nil {
		return nil, ctxerrors.Wrap(err, "decode final JSON answer")
	}

	if err := schema.Validate(value); err != nil {
		return nil, ctxerrors.Wrap(err, "answer does not match output schema")
	}

	return json.RawMessage(text), nil
}
