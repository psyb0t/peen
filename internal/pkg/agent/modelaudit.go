package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	modelAuditFailureClass       = "model"
	modelAuditRetryCostStartSize = 1
)

// modelAuditOptions identifies one logical Elelem invocation. Settings are
// deliberately passed as JSON so session stays transport-neutral and never
// needs Elelem's types.
type modelAuditOptions struct {
	Store               *session.Store
	SessionID           uuid.UUID
	TurnID              uuid.UUID
	AgentRunID          *uuid.UUID
	Stage               models.ModelRunStage
	ModelReference      string
	Model               elelem.Model
	RequestSettingsJSON string
	Now                 func() time.Time
}

// modelAuditRecorder persists both the logical invocation and every outbound
// provider round. Elelem invokes these callbacks serially for one Request, but
// the mutex makes that invariant explicit at the persistence boundary.
type modelAuditRecorder struct {
	store     *session.Store
	sessionID uuid.UUID
	model     elelem.Model
	now       func() time.Time
	run       *models.ModelRun

	mu          sync.Mutex
	activeRound int64
	calls       map[int64]*modelAuditCall
}

type modelAuditCall struct {
	id                  uuid.UUID
	responseMessageJSON string
	retries             []modelAuditRetryAttempt
	finished            bool
}

// modelAuditRequestSettings records every effective, non-secret request knob.
// Endpoint URLs and auth headers are intentionally absent. The qualified
// connection name is persisted separately on ModelRun.
type modelAuditRequestSettings struct {
	Model               elelem.Model   `json:"model"`
	AutoToolCalls       bool           `json:"autoToolCalls"`
	Streaming           bool           `json:"streaming"`
	MaxRounds           int            `json:"maxRounds"`
	MaxContextTokens    int            `json:"maxContextTokens"`
	MaxOutputTokens     int            `json:"maxOutputTokens"`
	MaxConcurrentTools  int            `json:"maxConcurrentTools"`
	ToolTimeoutMS       int64          `json:"toolTimeoutMs"`
	MaxToolResultTokens int            `json:"maxToolResultTokens"`
	TimeoutMS           int64          `json:"timeoutMs"`
	GenerationParams    map[string]any `json:"generationParams"`
}

// modelAuditTool is the provider-visible portion of an Elelem tool. Handler
// and hook function pointers cannot and must not become part of stored JSON.
type modelAuditTool struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ArgumentsSchema json.RawMessage `json:"argumentsSchema"`
	StrictArguments bool            `json:"strictArguments"`
	TimeoutMS       int64           `json:"timeoutMs"`
}

// modelAuditTokenCounts keeps every provider-reported token category, even
// categories that are subsets of prompt or completion tokens.
type modelAuditTokenCounts struct {
	Prompt            int64 `json:"prompt"`
	Completion        int64 `json:"completion"`
	Total             int64 `json:"total"`
	Reasoning         int64 `json:"reasoning"`
	CacheRead         int64 `json:"cacheRead"`
	CacheWrite        int64 `json:"cacheWrite"`
	CacheWriteLongTTL int64 `json:"cacheWriteLongTtl"`
}

type modelAuditRetryAttempt struct {
	Attempt  int                   `json:"attempt"`
	Reason   string                `json:"reason"`
	Error    string                `json:"error"`
	Status   int                   `json:"status"`
	DelayMS  int64                 `json:"delayMs"`
	Streamed bool                  `json:"streamed"`
	Tokens   modelAuditTokenCounts `json:"tokens"`
}

type modelAuditUsage struct {
	modelAuditTokenCounts
	Model                  string                   `json:"model"`
	FinishReason           string                   `json:"finishReason"`
	TotalAttempts          int                      `json:"totalAttempts"`
	FailedAttempts         []modelAuditRetryAttempt `json:"failedAttempts"`
	WastedPromptTokens     int64                    `json:"wastedPromptTokens"`
	WastedCompletionTokens int64                    `json:"wastedCompletionTokens"`
	WastedTotalTokens      int64                    `json:"wastedTotalTokens"`
}

// newModelAuditSettings serializes the effective public call configuration.
func newModelAuditSettings(
	model elelem.Model,
	autoToolCalls bool,
	streaming bool,
	maxRounds int,
	maxContextTokens int,
	maxOutputTokens int,
	maxConcurrentTools int,
	toolTimeout time.Duration,
	maxToolResultTokens int,
	timeout time.Duration,
) (string, error) {
	encoded, err := json.Marshal(modelAuditRequestSettings{
		Model:               model,
		AutoToolCalls:       autoToolCalls,
		Streaming:           streaming,
		MaxRounds:           maxRounds,
		MaxContextTokens:    maxContextTokens,
		MaxOutputTokens:     maxOutputTokens,
		MaxConcurrentTools:  maxConcurrentTools,
		ToolTimeoutMS:       toolTimeout.Milliseconds(),
		MaxToolResultTokens: maxToolResultTokens,
		TimeoutMS:           timeout.Milliseconds(),
		GenerationParams:    map[string]any{},
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "marshal model audit request settings")
	}

	return string(encoded), nil
}

func newModelAuditRecorder(
	ctx context.Context,
	options modelAuditOptions,
) (*modelAuditRecorder, error) {
	if options.Store == nil || options.SessionID == uuid.Nil ||
		options.TurnID == uuid.Nil || strings.TrimSpace(options.ModelReference) == "" ||
		strings.TrimSpace(options.Model.ID) == "" || options.Now == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model audit recorder")
	}

	connectionName, requestedModelID, found := strings.Cut(options.ModelReference, "/")
	if !found || connectionName == "" || requestedModelID == "" {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"model audit reference %q",
			options.ModelReference,
		)
	}

	run, err := options.Store.CreateModelRun(
		context.WithoutCancel(ctx),
		options.SessionID,
		session.CreateModelRunInput{
			TurnID:              options.TurnID,
			AgentRunID:          options.AgentRunID,
			Stage:               options.Stage,
			ModelReference:      options.ModelReference,
			ConnectionName:      connectionName,
			RequestedModelID:    requestedModelID,
			RequestSettingsJSON: options.RequestSettingsJSON,
			StartedAt:           options.Now(),
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create model audit run")
	}

	return &modelAuditRecorder{
		store:       options.Store,
		sessionID:   options.SessionID,
		model:       options.Model,
		now:         options.Now,
		run:         run,
		activeRound: -1,
		calls:       make(map[int64]*modelAuditCall),
	}, nil
}

// onRoundStart saves the exact messages and provider-visible tool definitions
// immediately before that round calls the model.
func (r *modelAuditRecorder) onRoundStart(
	ctx context.Context,
	event *elelem.RoundEvent,
) error {
	if event == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model audit round event")
	}

	requestMessagesJSON, err := modelAuditJSONArray(
		event.Messages,
		"round messages",
	)
	if err != nil {
		return err
	}
	requestToolsJSON, err := modelAuditToolsJSON(event.Tools)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.calls[int64(event.Round)]; exists {
		return ctxerrors.Wrap(commerr.ErrAlreadyExists, "model audit round")
	}

	stored, err := r.store.CreateModelCall(
		context.WithoutCancel(ctx),
		r.sessionID,
		r.run.ID,
		session.CreateModelCallInput{
			Round:               int64(event.Round),
			RequestMessagesJSON: requestMessagesJSON,
			RequestToolsJSON:    requestToolsJSON,
			StartedAt:           r.now(),
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "create model audit round")
	}

	r.calls[int64(event.Round)] = &modelAuditCall{id: stored.ID}
	r.activeRound = int64(event.Round)

	return nil
}

// onAssistantMessage saves the complete assistant output for the active round
// before Elelem advances to its round-end callback.
func (r *modelAuditRecorder) onAssistantMessage(
	ctx context.Context,
	message elelem.Message,
) error {
	encoded, err := modelAuditJSON(message, "round assistant message")
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.activeCall()
	if err != nil {
		return err
	}
	call.responseMessageJSON = encoded

	return nil
}

// onRetry checkpoints each failed attempt instead of waiting for the round to
// end. A crash during provider backoff therefore still leaves an audit trail.
func (r *modelAuditRecorder) onRetry(
	ctx context.Context,
	attempt elelem.RetryAttempt,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.activeCall()
	if err != nil {
		return err
	}

	call.retries = append(call.retries, modelAuditRetryAttemptFromElelem(attempt))
	retriesJSON, err := modelAuditJSONArray(
		call.retries,
		"model retry attempts",
	)
	if err != nil {
		return err
	}
	if err := r.store.RecordModelCallRetries(
		context.WithoutCancel(ctx),
		r.sessionID,
		r.run.ID,
		call.id,
		session.RecordModelCallRetriesInput{
			RetryAttemptsJSON: retriesJSON,
			RetryAttemptCount: int64(len(call.retries)),
		},
	); err != nil {
		return ctxerrors.Wrap(err, "checkpoint model audit retries")
	}

	return nil
}

// onRoundEnd finalizes a provider round with its exact token and retry usage.
func (r *modelAuditRecorder) onRoundEnd(
	ctx context.Context,
	event *elelem.RoundEvent,
) error {
	if event == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model audit round result")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	call, ok := r.calls[int64(event.Round)]
	if !ok {
		return ctxerrors.Wrap(commerr.ErrNotFound, "model audit round")
	}
	if call.finished {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "model audit round already finalized")
	}

	input, err := r.modelCallFinalizeInput(
		models.ModelCallStateCompleted,
		call.responseMessageJSON,
		event.Usage,
		call.retries,
		"",
		"",
	)
	if err != nil {
		return err
	}
	if _, err := r.store.FinalizeModelCall(
		context.WithoutCancel(ctx),
		r.sessionID,
		r.run.ID,
		call.id,
		input,
	); err != nil {
		return ctxerrors.Wrap(err, "finalize model audit round")
	}

	call.finished = true
	r.activeRound = -1

	return nil
}

// finish closes any active round and the logical model run after Request.Run
// returns, whether the driver completed, failed, or its context was cancelled.
func (r *modelAuditRecorder) finish(
	ctx context.Context,
	response *elelem.Response,
	runErr error,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	runState, callState, classification := modelAuditOutcome(runErr)
	if r.activeRound >= 0 {
		call, err := r.activeCall()
		if err != nil {
			return err
		}
		if !call.finished {
			usage := elelem.Usage{}
			if response != nil {
				usage = response.Usage
			}
			input, inputErr := r.modelCallFinalizeInput(
				callState,
				call.responseMessageJSON,
				usage,
				call.retries,
				classification,
				errorText(runErr),
			)
			if inputErr != nil {
				return inputErr
			}
			if _, err := r.store.FinalizeModelCall(
				context.WithoutCancel(ctx),
				r.sessionID,
				r.run.ID,
				call.id,
				input,
			); err != nil {
				return ctxerrors.Wrap(err, "finalize interrupted model audit round")
			}
			call.finished = true
		}
	}

	input, err := r.modelRunFinalizeInput(runState, response, classification, runErr)
	if err != nil {
		return err
	}
	if _, err := r.store.FinalizeModelRun(
		context.WithoutCancel(ctx),
		r.sessionID,
		r.run.ID,
		input,
	); err != nil {
		return ctxerrors.Wrap(err, "finalize model audit run")
	}

	return nil
}

func (r *modelAuditRecorder) activeCall() (*modelAuditCall, error) {
	if r.activeRound < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState, "model audit has no active round")
	}
	call, found := r.calls[r.activeRound]
	if !found {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "active model audit round")
	}

	return call, nil
}

func (r *modelAuditRecorder) modelCallFinalizeInput(
	state models.ModelCallState,
	responseMessageJSON string,
	usage elelem.Usage,
	retries []modelAuditRetryAttempt,
	failureClassification string,
	failureDetail string,
) (session.FinalizeModelCallInput, error) {
	if responseMessageJSON == "" {
		responseMessageJSON = "null"
	}
	usageJSON, err := modelAuditJSON(modelAuditUsageFromElelem(usage), "model round usage")
	if err != nil {
		return session.FinalizeModelCallInput{}, err
	}
	if len(retries) == 0 {
		retries = modelAuditRetryAttemptsFromElelem(usage.Retry.FailedAttempts)
	}
	retriesJSON, err := modelAuditJSONArray(retries, "model round retries")
	if err != nil {
		return session.FinalizeModelCallInput{}, err
	}
	costs, err := modelAuditCosts(r.model, usage)
	if err != nil {
		return session.FinalizeModelCallInput{}, err
	}

	return session.FinalizeModelCallInput{
		State:                   state,
		ResponseMessageJSON:     responseMessageJSON,
		ResponseUsageJSON:       usageJSON,
		RetryAttemptsJSON:       retriesJSON,
		RetryAttemptCount:       int64(len(retries)),
		PromptTokens:            usage.Prompt,
		CompletionTokens:        usage.Completion,
		TotalTokens:             usage.Total,
		ReasoningTokens:         usage.Reasoning,
		CacheReadTokens:         usage.CacheRead,
		CacheWriteTokens:        usage.CacheWrite,
		CacheWriteLongTTLTokens: usage.CacheWriteLongTTL,
		WastedPromptTokens:      usage.Retry.WastedPromptTokens,
		WastedCompletionTokens:  usage.Retry.WastedCompletionTokens,
		WastedTotalTokens:       usage.Retry.WastedTotalTokens,
		TotalAttempts:           int64(usage.Retry.TotalAttempts),
		ResponseModelID:         usage.Model,
		FinishReason:            string(usage.FinishReason),
		ResponseCostAmount:      costs.response,
		RetryCostAmount:         costs.retry,
		BilledCostAmount:        costs.billed,
		CostKnown:               costs.known,
		FailureClassification:   failureClassification,
		FailureDetail:           failureDetail,
	}, nil
}

func (r *modelAuditRecorder) modelRunFinalizeInput(
	state models.ModelRunState,
	response *elelem.Response,
	failureClassification string,
	runErr error,
) (session.FinalizeModelRunInput, error) {
	input := session.FinalizeModelRunInput{
		State:                  state,
		ResponseMessagesJSON:   "[]",
		ResponseInjectionsJSON: "[]",
		ResponseUsageJSON:      "{}",
		FailureClassification:  failureClassification,
		FailureDetail:          errorText(runErr),
	}
	if response == nil {
		return input, nil
	}

	messagesJSON, err := modelAuditJSONArray(
		response.Messages,
		"model run response messages",
	)
	if err != nil {
		return session.FinalizeModelRunInput{}, err
	}
	injectionsJSON, err := modelAuditJSONArray(
		response.Injections,
		"model run response injections",
	)
	if err != nil {
		return session.FinalizeModelRunInput{}, err
	}
	usageJSON, err := modelAuditJSON(modelAuditUsageFromElelem(response.Usage), "model run usage")
	if err != nil {
		return session.FinalizeModelRunInput{}, err
	}
	costs, err := modelAuditCosts(r.model, response.Usage)
	if err != nil {
		return session.FinalizeModelRunInput{}, err
	}

	input.ResponseModelID = response.Model
	input.ResponseText = response.Text
	input.ResponseThinking = response.Reasoning
	input.ResponseMessagesJSON = messagesJSON
	input.ResponseInjectionsJSON = injectionsJSON
	input.ResponseUsageJSON = usageJSON
	input.ResponseCostAmount = costs.response
	input.RetryCostAmount = costs.retry
	input.BilledCostAmount, err = sumModelCostAmounts(
		input.ResponseCostAmount,
		input.RetryCostAmount,
	)
	if err != nil {
		return session.FinalizeModelRunInput{}, err
	}
	input.CostKnown = costs.known
	input.FinishReason = string(response.FinishReason)

	return input, nil
}

type modelAuditCost struct {
	response string
	retry    string
	billed   string
	known    bool
}

func modelAuditCosts(model elelem.Model, usage elelem.Usage) (modelAuditCost, error) {
	known := modelPricingKnown(model.Pricing)
	if !known {
		return modelAuditCost{}, nil
	}

	response, err := modelUsageCostAmount(model, usage)
	if err != nil {
		return modelAuditCost{}, ctxerrors.Wrap(err, "calculate model response cost")
	}
	retryAmounts := make([]string, 0, len(usage.Retry.FailedAttempts)+modelAuditRetryCostStartSize)
	for _, attempt := range usage.Retry.FailedAttempts {
		amount, amountErr := modelUsageCostAmount(
			model,
			elelem.Usage{TokenCounts: attempt.Tokens},
		)
		if amountErr != nil {
			return modelAuditCost{}, ctxerrors.Wrap(amountErr, "calculate model retry cost")
		}
		retryAmounts = append(
			retryAmounts,
			amount,
		)
	}
	retry, err := sumModelCostAmounts(retryAmounts...)
	if err != nil {
		return modelAuditCost{}, ctxerrors.Wrap(err, "sum model retry cost")
	}
	billed, err := sumModelCostAmounts(response, retry)
	if err != nil {
		return modelAuditCost{}, ctxerrors.Wrap(err, "sum model billed cost")
	}

	return modelAuditCost{
		response: response,
		retry:    retry,
		billed:   billed,
		known:    true,
	}, nil
}

func modelPricingKnown(pricing elelem.ModelPricing) bool {
	return pricing.InputPerToken != 0 || pricing.OutputPerToken != 0 ||
		pricing.CacheReadPerToken != 0 || pricing.CacheWritePerToken != 0 ||
		pricing.CacheWriteLongTTLPerToken != 0 ||
		pricing.LongContextInputPerToken != 0 ||
		pricing.LongContextOutputPerToken != 0
}

func sumModelCostAmounts(amounts ...string) (string, error) {
	total := new(big.Rat)
	found := false
	for _, amount := range amounts {
		if amount == "" {
			continue
		}
		value, ok := new(big.Rat).SetString(amount)
		if !ok {
			return "", ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"parse model cost amount %q",
				amount,
			)
		}
		total.Add(total, value)
		found = true
	}
	if !found {
		return "", nil
	}

	return modelCostRatString(total)
}

// modelUsageCostAmount repeats Elelem's per-round pricing in decimal space.
// Elelem exposes rates as float64, but accounting must not persist binary
// floating-point noise such as 0.00007000000000000001.
func modelUsageCostAmount(model elelem.Model, usage elelem.Usage) (string, error) {
	inputRate, outputRate := model.Pricing.InputPerToken, model.Pricing.OutputPerToken
	if model.Pricing.LongContextThreshold > 0 &&
		usage.Prompt > int64(model.Pricing.LongContextThreshold) {
		if model.Pricing.LongContextInputPerToken != 0 {
			inputRate = model.Pricing.LongContextInputPerToken
		}
		if model.Pricing.LongContextOutputPerToken != 0 {
			outputRate = model.Pricing.LongContextOutputPerToken
		}
	}

	cacheReadRate := model.Pricing.CacheReadPerToken
	if cacheReadRate == 0 {
		cacheReadRate = inputRate
	}
	cacheWriteRate := model.Pricing.CacheWritePerToken
	if cacheWriteRate == 0 {
		cacheWriteRate = inputRate
	}
	longTTLRate := model.Pricing.CacheWriteLongTTLPerToken
	if longTTLRate == 0 {
		longTTLRate = cacheWriteRate
	}

	input, err := modelCostRate(inputRate)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse model input rate")
	}
	output, err := modelCostRate(outputRate)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse model output rate")
	}
	cacheRead, err := modelCostRate(cacheReadRate)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse model cache-read rate")
	}
	cacheWrite, err := modelCostRate(cacheWriteRate)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse model cache-write rate")
	}
	longTTL, err := modelCostRate(longTTLRate)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse model long-TTL cache-write rate")
	}

	longTTLWrite := min(usage.CacheWriteLongTTL, usage.CacheWrite)
	shortTTLWrite := usage.CacheWrite - longTTLWrite
	uncached := max(usage.Prompt-usage.CacheRead-usage.CacheWrite, 0)
	total := new(big.Rat)
	modelCostAdd(total, uncached, input)
	modelCostAdd(total, usage.CacheRead, cacheRead)
	modelCostAdd(total, shortTTLWrite, cacheWrite)
	modelCostAdd(total, longTTLWrite, longTTL)
	modelCostAdd(total, usage.Completion, output)

	return modelCostRatString(total)
}

func modelCostRate(value float64) (*big.Rat, error) {
	formatted := strconv.FormatFloat(value, 'f', -1, 64)
	rate, ok := new(big.Rat).SetString(formatted)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"parse model price %q",
			formatted,
		)
	}

	return rate, nil
}

func modelCostAdd(total *big.Rat, tokens int64, rate *big.Rat) {
	if tokens == 0 {
		return
	}

	total.Add(total, new(big.Rat).Mul(new(big.Rat).SetInt64(tokens), rate))
}

func modelCostRatString(value *big.Rat) (string, error) {
	denominator := new(big.Int).Set(value.Denom())
	for _, factor := range [...]int64{2, 5} {
		factorInteger := big.NewInt(factor)
		for new(big.Int).Mod(denominator, factorInteger).Sign() == 0 {
			denominator.Quo(denominator, factorInteger)
		}
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"model cost is not a finite decimal",
		)
	}

	precision := max(
		modelCostFactorCount(value.Denom(), 2),
		modelCostFactorCount(value.Denom(), 5),
	)
	formatted := value.FloatString(precision)
	if !strings.Contains(formatted, ".") {
		return formatted, nil
	}

	return strings.TrimSuffix(strings.TrimRight(formatted, "0"), "."), nil
}

func modelCostFactorCount(value *big.Int, factor int64) int {
	remainder := new(big.Int).Set(value)
	factorInteger := big.NewInt(factor)
	count := 0
	for new(big.Int).Mod(remainder, factorInteger).Sign() == 0 {
		remainder.Quo(remainder, factorInteger)
		count++
	}

	return count
}

func modelAuditToolsJSON(tools []elelem.Tool) (string, error) {
	auditTools := make([]modelAuditTool, 0, len(tools))
	for _, tool := range tools {
		auditTools = append(auditTools, modelAuditTool{
			Name:            tool.Name,
			Description:     tool.Description,
			ArgumentsSchema: tool.ArgumentsSchema,
			StrictArguments: tool.StrictArguments,
			TimeoutMS:       tool.Timeout.Milliseconds(),
		})
	}

	return modelAuditJSONArray(auditTools, "round tools")
}

func modelAuditJSON(value any, label string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ctxerrors.Wrap(err, "marshal model audit "+label)
	}

	return string(encoded), nil
}

func modelAuditJSONArray(value any, label string) (string, error) {
	encoded, err := modelAuditJSON(value, label)
	if err != nil {
		return "", err
	}
	if encoded == "null" {
		return "[]", nil
	}

	var decoded []json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		return "", ctxerrors.Wrapf(err, "decode model audit %s array", label)
	}
	if decoded == nil {
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"model audit %s must be an array",
			label,
		)
	}

	return encoded, nil
}

func modelAuditOutcome(err error) (
	models.ModelRunState,
	models.ModelCallState,
	string,
) {
	if err == nil {
		return models.ModelRunStateCompleted, models.ModelCallStateCompleted, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return models.ModelRunStateCancelled,
			models.ModelCallStateCancelled,
			failureClassCancelled
	}

	return models.ModelRunStateFailed, models.ModelCallStateFailed, modelAuditFailureClass
}

func modelAuditUsageFromElelem(usage elelem.Usage) modelAuditUsage {
	return modelAuditUsage{
		modelAuditTokenCounts:  modelAuditTokenCountsFromElelem(usage.TokenCounts),
		Model:                  usage.Model,
		FinishReason:           string(usage.FinishReason),
		TotalAttempts:          usage.Retry.TotalAttempts,
		FailedAttempts:         modelAuditRetryAttemptsFromElelem(usage.Retry.FailedAttempts),
		WastedPromptTokens:     usage.Retry.WastedPromptTokens,
		WastedCompletionTokens: usage.Retry.WastedCompletionTokens,
		WastedTotalTokens:      usage.Retry.WastedTotalTokens,
	}
}

func modelAuditRetryAttemptsFromElelem(
	attempts []elelem.RetryAttempt,
) []modelAuditRetryAttempt {
	converted := make([]modelAuditRetryAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		converted = append(converted, modelAuditRetryAttemptFromElelem(attempt))
	}

	return converted
}

func modelAuditRetryAttemptFromElelem(
	attempt elelem.RetryAttempt,
) modelAuditRetryAttempt {
	return modelAuditRetryAttempt{
		Attempt:  attempt.Attempt,
		Reason:   attempt.Reason,
		Error:    errorText(attempt.Err),
		Status:   attempt.Status,
		DelayMS:  attempt.Delay.Milliseconds(),
		Streamed: attempt.Streamed,
		Tokens:   modelAuditTokenCountsFromElelem(attempt.Tokens),
	}
}

func modelAuditTokenCountsFromElelem(
	tokens elelem.TokenCounts,
) modelAuditTokenCounts {
	return modelAuditTokenCounts{
		Prompt:            tokens.Prompt,
		Completion:        tokens.Completion,
		Total:             tokens.Total,
		Reasoning:         tokens.Reasoning,
		CacheRead:         tokens.CacheRead,
		CacheWrite:        tokens.CacheWrite,
		CacheWriteLongTTL: tokens.CacheWriteLongTTL,
	}
}
