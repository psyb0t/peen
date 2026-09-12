package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const (
	defaultModelRunResponseMessagesJSON   = "[]"
	defaultModelRunResponseInjectionsJSON = "[]"
	defaultModelRunResponseUsageJSON      = "{}"
	defaultModelCallResponseMessageJSON   = "null"
	defaultModelCallResponseUsageJSON     = "{}"
	defaultModelCallRetryAttemptsJSON     = "[]"

	interruptedModelRunFailureDetail = "process stopped before model run completed"
)

// CreateModelRun records a logical model invocation before any provider work
// begins. No live event is permitted to outrun this row.
func (s *Store) CreateModelRun(
	ctx context.Context,
	sessionID uuid.UUID,
	input CreateModelRunInput,
) (*models.ModelRun, error) {
	if err := validateCreateModelRunInput(sessionID, input); err != nil {
		return nil, err
	}

	runID := input.ID
	if runID == uuid.Nil {
		runID = s.newID()
	}
	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = s.now()
	} else {
		startedAt = startedAt.UTC()
	}

	result := &models.ModelRun{
		ID:                     runID,
		SessionID:              sessionID,
		TurnID:                 input.TurnID,
		AgentRunID:             input.AgentRunID,
		Stage:                  input.Stage,
		ModelReference:         input.ModelReference,
		ConnectionName:         input.ConnectionName,
		RequestedModelID:       input.RequestedModelID,
		RequestSettingsJSON:    input.RequestSettingsJSON,
		State:                  models.ModelRunStateRunning,
		ResponseMessagesJSON:   defaultModelRunResponseMessagesJSON,
		ResponseInjectionsJSON: defaultModelRunResponseInjectionsJSON,
		ResponseUsageJSON:      defaultModelRunResponseUsageJSON,
		StartedAt:              startedAt,
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findSessionWithQuery(ctx, tx, sessionID); err != nil {
			return err
		}
		turn := tx.Turn
		if _, err := turn.WithContext(ctx).
			Where(turn.ID.Eq(input.TurnID), turn.SessionID.Eq(sessionID)).
			First(); err != nil {
			return ctxerrors.Wrap(err, "find parent turn for model run")
		}
		if input.AgentRunID != nil {
			agentRun := tx.AgentRun
			if _, err := agentRun.WithContext(ctx).
				Where(
					agentRun.ID.Eq(*input.AgentRunID),
					agentRun.SessionID.Eq(sessionID),
				).
				First(); err != nil {
				return ctxerrors.Wrap(err, "find parent agent run for model run")
			}
		}
		if err := tx.ModelRun.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create model run")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "create durable model run")
	}

	return result, nil
}

// CreateModelCall writes one exact outbound provider request before Elelem
// gives its driver permission to execute it.
func (s *Store) CreateModelCall(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input CreateModelCallInput,
) (*models.ModelCall, error) {
	if err := validateCreateModelCallInput(sessionID, modelRunID, input); err != nil {
		return nil, err
	}

	callID := input.ID
	if callID == uuid.Nil {
		callID = s.newID()
	}
	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = s.now()
	} else {
		startedAt = startedAt.UTC()
	}
	result := &models.ModelCall{
		ID:                  callID,
		SessionID:           sessionID,
		ModelRunID:          modelRunID,
		Round:               input.Round,
		RequestMessagesJSON: input.RequestMessagesJSON,
		RequestToolsJSON:    input.RequestToolsJSON,
		State:               models.ModelCallStateRunning,
		ResponseMessageJSON: defaultModelCallResponseMessageJSON,
		ResponseUsageJSON:   defaultModelCallResponseUsageJSON,
		RetryAttemptsJSON:   defaultModelCallRetryAttemptsJSON,
		StartedAt:           startedAt,
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run, err := s.findModelRunWithQuery(ctx, tx, sessionID, modelRunID)
		if err != nil {
			return err
		}
		if run.State != models.ModelRunStateRunning {
			return ctxerrors.Wrap(commerr.ErrInvalidState, "model call parent is not running")
		}
		if err := tx.ModelCall.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create model call")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "create durable model call")
	}

	return result, nil
}

// RecordModelCallRetries checkpoints failed attempts while the provider round
// remains in progress, preserving them even if the process exits mid-backoff.
func (s *Store) RecordModelCallRetries(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input RecordModelCallRetriesInput,
) error {
	if err := validateRecordModelCallRetriesInput(
		sessionID,
		modelRunID,
		modelCallID,
		input,
	); err != nil {
		return err
	}

	call, err := s.findModelCallWithQuery(ctx, s.query, sessionID, modelRunID, modelCallID)
	if err != nil {
		return ctxerrors.Wrap(err, "find model call for retry record")
	}
	if call.State != models.ModelCallStateRunning {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "model call retry after terminal state")
	}

	repository := s.query.ModelCall
	updated, err := repository.WithContext(ctx).
		Where(
			repository.ID.Eq(modelCallID),
			repository.SessionID.Eq(sessionID),
			repository.ModelRunID.Eq(modelRunID),
			repository.State.Eq(string(models.ModelCallStateRunning)),
		).
		UpdateSimple(
			repository.RetryAttemptsJSON.Value(input.RetryAttemptsJSON),
			repository.RetryAttemptCount.Value(input.RetryAttemptCount),
		)
	if err != nil {
		return ctxerrors.Wrap(err, "checkpoint model call retries")
	}
	if updated.RowsAffected != 1 {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "model call retry checkpoint")
	}

	return nil
}

// FinalizeModelCall writes one provider round's output and accounting exactly
// once. The request snapshot is immutable from CreateModelCall.
func (s *Store) FinalizeModelCall(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input FinalizeModelCallInput,
) (*models.ModelCall, error) {
	if err := validateFinalizeModelCallInput(sessionID, modelRunID, modelCallID, input); err != nil {
		return nil, err
	}

	var result *models.ModelCall
	if err := s.query.Transaction(func(tx *repositories.Query) error {
		call, err := s.findModelCallWithQuery(ctx, tx, sessionID, modelRunID, modelCallID)
		if err != nil {
			return err
		}
		if call.State != models.ModelCallStateRunning {
			return ctxerrors.Wrap(commerr.ErrInvalidState, "model call is not running")
		}

		now := s.now()
		repository := tx.ModelCall
		updated, err := repository.WithContext(ctx).
			Where(
				repository.ID.Eq(modelCallID),
				repository.SessionID.Eq(sessionID),
				repository.ModelRunID.Eq(modelRunID),
				repository.State.Eq(string(models.ModelCallStateRunning)),
			).
			UpdateSimple(
				repository.State.Value(string(input.State)),
				repository.ResponseMessageJSON.Value(input.ResponseMessageJSON),
				repository.ResponseUsageJSON.Value(input.ResponseUsageJSON),
				repository.RetryAttemptsJSON.Value(input.RetryAttemptsJSON),
				repository.RetryAttemptCount.Value(input.RetryAttemptCount),
				repository.PromptTokens.Value(input.PromptTokens),
				repository.CompletionTokens.Value(input.CompletionTokens),
				repository.TotalTokens.Value(input.TotalTokens),
				repository.ReasoningTokens.Value(input.ReasoningTokens),
				repository.CacheReadTokens.Value(input.CacheReadTokens),
				repository.CacheWriteTokens.Value(input.CacheWriteTokens),
				repository.CacheWriteLongTTLTokens.Value(input.CacheWriteLongTTLTokens),
				repository.WastedPromptTokens.Value(input.WastedPromptTokens),
				repository.WastedCompletionTokens.Value(input.WastedCompletionTokens),
				repository.WastedTotalTokens.Value(input.WastedTotalTokens),
				repository.TotalAttempts.Value(input.TotalAttempts),
				repository.ResponseModelID.Value(input.ResponseModelID),
				repository.FinishReason.Value(input.FinishReason),
				repository.ResponseCostAmount.Value(input.ResponseCostAmount),
				repository.RetryCostAmount.Value(input.RetryCostAmount),
				repository.BilledCostAmount.Value(input.BilledCostAmount),
				repository.CostKnown.Value(input.CostKnown),
				repository.FailureClassification.Value(input.FailureClassification),
				repository.FailureDetail.Value(input.FailureDetail),
				repository.CompletedAt.Value(now),
			)
		if err != nil {
			return ctxerrors.Wrap(err, "finalize model call")
		}
		if updated.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrInvalidState, "model call terminal update")
		}

		applyModelCallFinalization(call, input, now)
		result = call

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize durable model call")
	}

	return result, nil
}

// FinalizeModelRun writes the logical invocation outcome after every round has
// either finished or been marked terminal.
func (s *Store) FinalizeModelRun(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input FinalizeModelRunInput,
) (*models.ModelRun, error) {
	if err := validateFinalizeModelRunInput(sessionID, modelRunID, input); err != nil {
		return nil, err
	}

	var result *models.ModelRun
	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run, err := s.findModelRunWithQuery(ctx, tx, sessionID, modelRunID)
		if err != nil {
			return err
		}
		if run.State != models.ModelRunStateRunning {
			return ctxerrors.Wrap(commerr.ErrInvalidState, "model run is not running")
		}

		now := s.now()
		repository := tx.ModelRun
		updated, err := repository.WithContext(ctx).
			Where(
				repository.ID.Eq(modelRunID),
				repository.SessionID.Eq(sessionID),
				repository.State.Eq(string(models.ModelRunStateRunning)),
			).
			UpdateSimple(
				repository.State.Value(string(input.State)),
				repository.ResponseModelID.Value(input.ResponseModelID),
				repository.ResponseText.Value(input.ResponseText),
				repository.ResponseThinking.Value(input.ResponseThinking),
				repository.ResponseMessagesJSON.Value(input.ResponseMessagesJSON),
				repository.ResponseInjectionsJSON.Value(input.ResponseInjectionsJSON),
				repository.ResponseUsageJSON.Value(input.ResponseUsageJSON),
				repository.ResponseCostAmount.Value(input.ResponseCostAmount),
				repository.RetryCostAmount.Value(input.RetryCostAmount),
				repository.BilledCostAmount.Value(input.BilledCostAmount),
				repository.CostKnown.Value(input.CostKnown),
				repository.FinishReason.Value(input.FinishReason),
				repository.FailureClassification.Value(input.FailureClassification),
				repository.FailureDetail.Value(input.FailureDetail),
				repository.CompletedAt.Value(now),
			)
		if err != nil {
			return ctxerrors.Wrap(err, "finalize model run")
		}
		if updated.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrInvalidState, "model run terminal update")
		}

		applyModelRunFinalization(run, input, now)
		result = run

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize durable model run")
	}

	return result, nil
}

// GetModelRun returns one session-scoped logical model invocation.
func (s *Store) GetModelRun(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
) (*models.ModelRun, error) {
	run, err := s.findModelRunWithQuery(ctx, s.query, sessionID, modelRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get model run")
	}

	return run, nil
}

// ListModelRuns returns a stable newest-first session-local model run page.
func (s *Store) ListModelRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListModelRunsOptions,
) (*ModelRunPage, error) {
	options, err := normalizeModelRunListOptions(options)
	if err != nil {
		return nil, err
	}
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for model run listing")
	}

	run := s.query.ModelRun
	query := run.WithContext(ctx).Where(run.SessionID.Eq(sessionID))
	if options.Stage != nil {
		query = query.Where(run.Stage.Eq(string(*options.Stage)))
	}
	if options.State != nil {
		query = query.Where(run.State.Eq(string(*options.State)))
	}

	items, err := query.
		Order(run.StartedAt.Desc(), run.ID.Desc()).
		Offset(options.Offset).
		Limit(options.Limit).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list model runs")
	}

	probe, err := query.
		Order(run.StartedAt.Desc(), run.ID.Desc()).
		Offset(options.Offset + options.Limit).
		Limit(1).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe model run page continuation")
	}

	return &ModelRunPage{
		Items:   items,
		Limit:   options.Limit,
		Offset:  options.Offset,
		HasMore: len(probe) > 0,
	}, nil
}

// ListModelCalls returns the ordered provider rounds for one durable model
// invocation, including each exact request and retry trail.
func (s *Store) ListModelCalls(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	options ListModelCallsOptions,
) (*ModelCallPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}
	run, err := s.GetModelRun(ctx, sessionID, modelRunID)
	if err != nil {
		return nil, err
	}

	call := s.query.ModelCall
	query := call.WithContext(ctx).
		Where(call.SessionID.Eq(sessionID), call.ModelRunID.Eq(modelRunID)).
		Order(call.Round.Asc(), call.ID.Asc())

	items, err := query.Offset(page.offset).Limit(page.limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list model calls")
	}
	probe, err := query.Offset(page.offset + page.limit).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe model call page continuation")
	}

	return &ModelCallPage{
		Run:     run,
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: len(probe) > 0,
	}, nil
}

// RecoverInterruptedModelRuns marks durable work left running by a prior Peen
// process terminal. No provider connection can be safely resumed in place.
func (s *Store) RecoverInterruptedModelRuns(ctx context.Context) (int, error) {
	var recovered int
	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run := tx.ModelRun
		running, err := run.WithContext(ctx).
			Where(run.State.Eq(string(models.ModelRunStateRunning))).
			Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list running model runs")
		}

		for _, item := range running {
			now := s.now()
			updated, updateErr := run.WithContext(ctx).
				Where(
					run.ID.Eq(item.ID),
					run.State.Eq(string(models.ModelRunStateRunning)),
				).
				UpdateSimple(
					run.State.Value(string(models.ModelRunStateInterrupted)),
					run.FailureClassification.Value(string(models.ModelRunStateInterrupted)),
					run.FailureDetail.Value(interruptedModelRunFailureDetail),
					run.CompletedAt.Value(now),
				)
			if updateErr != nil {
				return ctxerrors.Wrap(updateErr, "mark model run interrupted")
			}
			if updated.RowsAffected != 1 {
				continue
			}

			call := tx.ModelCall
			if _, err := call.WithContext(ctx).
				Where(
					call.ModelRunID.Eq(item.ID),
					call.State.Eq(string(models.ModelCallStateRunning)),
				).
				UpdateSimple(
					call.State.Value(string(models.ModelCallStateInterrupted)),
					call.FailureClassification.Value(string(models.ModelCallStateInterrupted)),
					call.FailureDetail.Value(interruptedModelRunFailureDetail),
					call.CompletedAt.Value(now),
				); err != nil {
				return ctxerrors.Wrap(err, "mark model call interrupted")
			}
			recovered++
		}

		return nil
	}); err != nil {
		return 0, ctxerrors.Wrap(err, "recover interrupted model runs")
	}

	return recovered, nil
}

func applyModelCallFinalization(
	call *models.ModelCall,
	input FinalizeModelCallInput,
	completedAt time.Time,
) {
	call.State = input.State
	call.ResponseMessageJSON = input.ResponseMessageJSON
	call.ResponseUsageJSON = input.ResponseUsageJSON
	call.RetryAttemptsJSON = input.RetryAttemptsJSON
	call.RetryAttemptCount = input.RetryAttemptCount
	call.PromptTokens = input.PromptTokens
	call.CompletionTokens = input.CompletionTokens
	call.TotalTokens = input.TotalTokens
	call.ReasoningTokens = input.ReasoningTokens
	call.CacheReadTokens = input.CacheReadTokens
	call.CacheWriteTokens = input.CacheWriteTokens
	call.CacheWriteLongTTLTokens = input.CacheWriteLongTTLTokens
	call.WastedPromptTokens = input.WastedPromptTokens
	call.WastedCompletionTokens = input.WastedCompletionTokens
	call.WastedTotalTokens = input.WastedTotalTokens
	call.TotalAttempts = input.TotalAttempts
	call.ResponseModelID = input.ResponseModelID
	call.FinishReason = input.FinishReason
	call.ResponseCostAmount = input.ResponseCostAmount
	call.RetryCostAmount = input.RetryCostAmount
	call.BilledCostAmount = input.BilledCostAmount
	call.CostKnown = input.CostKnown
	call.FailureClassification = input.FailureClassification
	call.FailureDetail = input.FailureDetail
	call.CompletedAt = &completedAt
}

func applyModelRunFinalization(
	run *models.ModelRun,
	input FinalizeModelRunInput,
	completedAt time.Time,
) {
	run.State = input.State
	run.ResponseModelID = input.ResponseModelID
	run.ResponseText = input.ResponseText
	run.ResponseThinking = input.ResponseThinking
	run.ResponseMessagesJSON = input.ResponseMessagesJSON
	run.ResponseInjectionsJSON = input.ResponseInjectionsJSON
	run.ResponseUsageJSON = input.ResponseUsageJSON
	run.ResponseCostAmount = input.ResponseCostAmount
	run.RetryCostAmount = input.RetryCostAmount
	run.BilledCostAmount = input.BilledCostAmount
	run.CostKnown = input.CostKnown
	run.FinishReason = input.FinishReason
	run.FailureClassification = input.FailureClassification
	run.FailureDetail = input.FailureDetail
	run.CompletedAt = &completedAt
}

func validateCreateModelRunInput(
	sessionID uuid.UUID,
	input CreateModelRunInput,
) error {
	if sessionID == uuid.Nil || input.TurnID == uuid.Nil ||
		strings.TrimSpace(input.ModelReference) == "" ||
		strings.TrimSpace(input.ConnectionName) == "" ||
		strings.TrimSpace(input.RequestedModelID) == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model run")
	}
	if !isModelRunStage(input.Stage) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model run stage")
	}
	if !json.Valid([]byte(input.RequestSettingsJSON)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model run request settings JSON")
	}

	return nil
}

func validateCreateModelCallInput(
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input CreateModelCallInput,
) error {
	if sessionID == uuid.Nil || modelRunID == uuid.Nil || input.Round < 0 {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model call")
	}
	if !json.Valid([]byte(input.RequestMessagesJSON)) ||
		!json.Valid([]byte(input.RequestToolsJSON)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model call request JSON")
	}

	return nil
}

func validateRecordModelCallRetriesInput(
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input RecordModelCallRetriesInput,
) error {
	if sessionID == uuid.Nil || modelRunID == uuid.Nil || modelCallID == uuid.Nil ||
		input.RetryAttemptCount < 0 {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model call retry")
	}
	if !json.Valid([]byte(input.RetryAttemptsJSON)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model call retries JSON")
	}

	return nil
}

func validateFinalizeModelCallInput(
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input FinalizeModelCallInput,
) error {
	if sessionID == uuid.Nil || modelRunID == uuid.Nil || modelCallID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model call finalization")
	}
	if !isModelCallTerminalState(input.State) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model call terminal state")
	}
	if input.RetryAttemptCount < 0 || input.PromptTokens < 0 ||
		input.CompletionTokens < 0 || input.TotalTokens < 0 ||
		input.ReasoningTokens < 0 || input.CacheReadTokens < 0 ||
		input.CacheWriteTokens < 0 || input.CacheWriteLongTTLTokens < 0 ||
		input.WastedPromptTokens < 0 || input.WastedCompletionTokens < 0 ||
		input.WastedTotalTokens < 0 || input.TotalAttempts < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model call token accounting")
	}
	if !json.Valid([]byte(input.ResponseMessageJSON)) ||
		!json.Valid([]byte(input.ResponseUsageJSON)) ||
		!json.Valid([]byte(input.RetryAttemptsJSON)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model call response JSON")
	}

	return nil
}

func validateFinalizeModelRunInput(
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input FinalizeModelRunInput,
) error {
	if sessionID == uuid.Nil || modelRunID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "model run finalization")
	}
	if !isModelRunTerminalState(input.State) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model run terminal state")
	}
	if !json.Valid([]byte(input.ResponseMessagesJSON)) ||
		!json.Valid([]byte(input.ResponseInjectionsJSON)) ||
		!json.Valid([]byte(input.ResponseUsageJSON)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "model run response JSON")
	}

	return nil
}

func normalizeModelRunListOptions(options ListModelRunsOptions) (ListModelRunsOptions, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return ListModelRunsOptions{}, err
	}
	options.Limit = page.limit
	options.Offset = page.offset
	if options.Stage != nil && !isModelRunStage(*options.Stage) {
		return ListModelRunsOptions{}, ctxerrors.Wrap(ErrInvalidPage, "model run stage")
	}
	if options.State != nil && !isModelRunState(*options.State) {
		return ListModelRunsOptions{}, ctxerrors.Wrap(ErrInvalidPage, "model run state")
	}

	return options, nil
}

func isModelRunStage(stage models.ModelRunStage) bool {
	return stage == models.ModelRunStageTurn ||
		stage == models.ModelRunStageChild ||
		stage == models.ModelRunStageCompaction
}

func isModelRunState(state models.ModelRunState) bool {
	return state == models.ModelRunStateRunning || isModelRunTerminalState(state)
}

func isModelRunTerminalState(state models.ModelRunState) bool {
	return state == models.ModelRunStateCompleted ||
		state == models.ModelRunStateFailed ||
		state == models.ModelRunStateCancelled ||
		state == models.ModelRunStateInterrupted
}

func isModelCallTerminalState(state models.ModelCallState) bool {
	return state == models.ModelCallStateCompleted ||
		state == models.ModelCallStateFailed ||
		state == models.ModelCallStateCancelled ||
		state == models.ModelCallStateInterrupted
}

func (s *Store) findModelRunWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
) (*models.ModelRun, error) {
	run, err := query.ModelRun.WithContext(ctx).
		Where(
			query.ModelRun.ID.Eq(modelRunID),
			query.ModelRun.SessionID.Eq(sessionID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query model run")
	}

	return run, nil
}

func (s *Store) findModelCallWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
) (*models.ModelCall, error) {
	call, err := query.ModelCall.WithContext(ctx).
		Where(
			query.ModelCall.ID.Eq(modelCallID),
			query.ModelCall.SessionID.Eq(sessionID),
			query.ModelCall.ModelRunID.Eq(modelRunID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query model call")
	}

	return call, nil
}
