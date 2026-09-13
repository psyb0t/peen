package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const defaultAgentRunResponseMessagesJSON = "[]"

// CreateAgentRun records a child before any provider work or visible stream
// event can begin.
func (s *Store) CreateAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	input StartAgentRunInput,
) (*models.AgentRun, error) {
	if err := validateAgentRunStart(sessionID, input); err != nil {
		return nil, err
	}

	result := s.newAgentRun(sessionID, input)

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if err := s.validateRunParents(
			ctx,
			tx,
			sessionID,
			input.ParentTurnID,
			input.ParentAgentRunID,
			"agent run",
		); err != nil {
			return err
		}

		if err := tx.AgentRun.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create agent run")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "create durable agent run")
	}

	return result, nil
}

func (s *Store) newAgentRun(
	sessionID uuid.UUID,
	input StartAgentRunInput,
) *models.AgentRun {
	result := &models.AgentRun{
		ID:                   input.ID,
		SessionID:            sessionID,
		ParentTurnID:         input.ParentTurnID,
		ParentAgentRunID:     input.ParentAgentRunID,
		ParentToolCallID:     input.ParentToolCallID,
		RequestID:            input.RequestID,
		Name:                 input.Name,
		Definition:           input.Definition,
		Depth:                input.Depth,
		Workspace:            input.Workspace,
		ModelReference:       input.ModelReference,
		ModelID:              input.ModelID,
		Task:                 input.Task,
		Instructions:         input.Instructions,
		AllowedToolsJSON:     input.AllowedToolsJSON,
		SystemPrompt:         input.SystemPrompt,
		State:                models.AgentRunStateRunning,
		ResponseMessagesJSON: defaultAgentRunResponseMessagesJSON,
		StartedAt:            input.StartedAt,
	}
	if result.ID == uuid.Nil {
		result.ID = s.newID()
	}

	if result.StartedAt.IsZero() {
		result.StartedAt = s.now()
	} else {
		result.StartedAt = result.StartedAt.UTC()
	}

	return result
}

func (s *Store) validateRunParents(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	parentAgentRunID *uuid.UUID,
	kind string,
) error {
	if _, err := s.findSessionWithQuery(ctx, query, sessionID); err != nil {
		return err
	}

	turn := query.Turn
	if _, err := turn.WithContext(ctx).
		Where(turn.ID.Eq(turnID), turn.SessionID.Eq(sessionID)).
		First(); err != nil {
		return ctxerrors.Wrap(err, "find parent turn for "+kind)
	}

	if parentAgentRunID == nil {
		return nil
	}

	parent := query.AgentRun
	if _, err := parent.WithContext(ctx).
		Where(
			parent.ID.Eq(*parentAgentRunID),
			parent.SessionID.Eq(sessionID),
		).
		First(); err != nil {
		return ctxerrors.Wrap(err, "find parent agent run for "+kind)
	}

	return nil
}

func nextSequence(latest func() (int64, error)) (int64, error) {
	sequence, err := latest()
	if err == nil {
		return sequence + 1, nil
	}

	if errors.Is(err, commerr.ErrNotFound) {
		return 1, nil
	}

	return 0, err
}

// AppendAgentRunEvent records a child event before it reaches a live client.
func (s *Store) AppendAgentRunEvent(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input AgentRunEventInput,
) (*models.AgentRunEvent, error) {
	input, err := s.normalizeAgentRunEventInput(input)
	if err != nil {
		return nil, err
	}

	var result *models.AgentRunEvent

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run, err := s.findAgentRunWithQuery(ctx, tx, sessionID, agentRunID)
		if err != nil {
			return err
		}

		event := tx.AgentRunEvent

		sequence, err := nextAgentRunEventSequence(ctx, tx, agentRunID)
		if err != nil {
			return ctxerrors.Wrap(err, "find latest agent run event")
		}

		result = &models.AgentRunEvent{
			ID:          input.ID,
			SessionID:   sessionID,
			AgentRunID:  agentRunID,
			Sequence:    sequence,
			EventType:   input.EventType,
			PayloadJSON: input.PayloadJSON,
			CreatedAt:   input.CreatedAt,
		}
		if err := event.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create agent run event")
		}

		if _, err := tx.AgentRun.WithContext(ctx).
			Where(tx.AgentRun.ID.Eq(run.ID)).
			UpdateSimple(tx.AgentRun.EventCount.Add(1)); err != nil {
			return ctxerrors.Wrap(err, "update agent run event count")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "append durable agent run event")
	}

	return result, nil
}

func (s *Store) normalizeAgentRunEventInput(
	input AgentRunEventInput,
) (AgentRunEventInput, error) {
	if strings.TrimSpace(input.EventType) == "" {
		return AgentRunEventInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent event type",
		)
	}

	if !json.Valid([]byte(input.PayloadJSON)) {
		return AgentRunEventInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent event payload JSON",
		)
	}

	if input.ID == uuid.Nil {
		input.ID = s.newID()
	}

	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	} else {
		input.CreatedAt = input.CreatedAt.UTC()
	}

	return input, nil
}

func nextAgentRunEventSequence(
	ctx context.Context,
	query *repositories.Query,
	agentRunID uuid.UUID,
) (int64, error) {
	event := query.AgentRunEvent

	return nextSequence(func() (int64, error) {
		latest, err := event.WithContext(ctx).
			Where(event.AgentRunID.Eq(agentRunID)).
			Order(event.Sequence.Desc(), event.ID.Desc()).
			First()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "query latest agent run event")
		}

		return latest.Sequence, nil
	})
}

// FinalizeAgentRun writes the terminal response or failure exactly once.
func (s *Store) FinalizeAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input FinalizeAgentRunInput,
) (*models.AgentRun, error) {
	input, err := normalizeAgentRunFinalization(input)
	if err != nil {
		return nil, err
	}

	var result *models.AgentRun

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		finalized, finalizeErr := s.finalizeAgentRunInTransaction(
			ctx,
			tx,
			sessionID,
			agentRunID,
			input,
		)
		if finalizeErr == nil {
			result = finalized
		}

		return finalizeErr
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize durable agent run")
	}

	return result, nil
}

func (s *Store) finalizeAgentRunInTransaction(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input FinalizeAgentRunInput,
) (*models.AgentRun, error) {
	run, err := s.findAgentRunWithQuery(ctx, query, sessionID, agentRunID)
	if err != nil {
		return nil, err
	}

	if run.State != models.AgentRunStateRunning {
		return nil, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"agent run is not running",
		)
	}

	now := s.now()
	if err := updateAgentRunFinalization(
		ctx,
		query,
		sessionID,
		agentRunID,
		input,
		now,
	); err != nil {
		return nil, err
	}

	applyAgentRunFinalization(run, input, now)

	return run, nil
}

func updateAgentRunFinalization(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input FinalizeAgentRunInput,
	endedAt time.Time,
) error {
	repository := query.AgentRun

	updated, err := repository.WithContext(ctx).
		Where(
			repository.ID.Eq(agentRunID),
			repository.SessionID.Eq(sessionID),
			repository.State.Eq(string(models.AgentRunStateRunning)),
		).
		UpdateSimple(
			repository.State.Value(string(input.State)),
			repository.ResponseText.Value(input.ResponseText),
			repository.ResponseThinking.Value(input.ResponseThinking),
			repository.ResponseMessagesJSON.Value(input.ResponseMessagesJSON),
			repository.FinishReason.Value(input.FinishReason),
			repository.PromptTokenCount.Value(input.PromptTokenCount),
			repository.CompletionTokenCount.Value(input.CompletionTokenCount),
			repository.FailureClassification.Value(input.FailureClassification),
			repository.FailureDetail.Value(input.FailureDetail),
			repository.EndedAt.Value(endedAt),
		)
	if err != nil {
		return ctxerrors.Wrap(err, "finalize agent run")
	}

	if updated.RowsAffected != 1 {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"agent run terminal update",
		)
	}

	return nil
}

func normalizeAgentRunFinalization(
	input FinalizeAgentRunInput,
) (FinalizeAgentRunInput, error) {
	if !isAgentRunTerminalState(input.State) {
		return FinalizeAgentRunInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent run terminal state",
		)
	}

	if input.ResponseMessagesJSON == "" {
		input.ResponseMessagesJSON = defaultAgentRunResponseMessagesJSON
	}

	if !json.Valid([]byte(input.ResponseMessagesJSON)) {
		return FinalizeAgentRunInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent response messages JSON",
		)
	}

	if input.PromptTokenCount < 0 || input.CompletionTokenCount < 0 {
		return FinalizeAgentRunInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent token count",
		)
	}

	return input, nil
}

func applyAgentRunFinalization(
	run *models.AgentRun,
	input FinalizeAgentRunInput,
	endedAt time.Time,
) {
	run.State = input.State
	run.ResponseText = input.ResponseText
	run.ResponseThinking = input.ResponseThinking
	run.ResponseMessagesJSON = input.ResponseMessagesJSON
	run.FinishReason = input.FinishReason
	run.PromptTokenCount = input.PromptTokenCount
	run.CompletionTokenCount = input.CompletionTokenCount
	run.FailureClassification = input.FailureClassification
	run.FailureDetail = input.FailureDetail
	run.EndedAt = &endedAt
}

// RequestAgentRunCancellation records cancellation before signalling an active
// in-process child. It is idempotent for terminal and already-requested rows.
func (s *Store) RequestAgentRunCancellation(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRun, bool, error) {
	var (
		result    *models.AgentRun
		requested bool
	)

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run, err := s.findAgentRunWithQuery(ctx, tx, sessionID, agentRunID)
		if err != nil {
			return err
		}

		result = run

		if run.State != models.AgentRunStateRunning || run.CancelRequested {
			return nil
		}

		repository := tx.AgentRun

		updated, err := repository.WithContext(ctx).
			Where(
				repository.ID.Eq(agentRunID),
				repository.SessionID.Eq(sessionID),
				repository.State.Eq(string(models.AgentRunStateRunning)),
				repository.CancelRequested.Is(false),
			).
			UpdateSimple(repository.CancelRequested.Value(true))
		if err != nil {
			return ctxerrors.Wrap(err, "request agent run cancellation")
		}

		if updated.RowsAffected == 0 {
			return nil
		}

		result.CancelRequested = true
		requested = true

		return nil
	}); err != nil {
		return nil, false, ctxerrors.Wrap(
			err,
			"request durable agent cancellation",
		)
	}

	return result, requested, nil
}

// GetAgentRun returns one session-scoped durable child run.
func (s *Store) GetAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRun, error) {
	run, err := s.findAgentRunWithQuery(ctx, s.query, sessionID, agentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get agent run")
	}

	return run, nil
}

// ListAgentRuns returns a stable newest-first session-scoped child-run page.
//
//nolint:dupl // The generated AgentRun query has a distinct typed builder.
func (s *Store) ListAgentRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListAgentRunsOptions,
) (*AgentRunPage, error) {
	options, err := normalizeAgentRunListOptions(options)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for agent run listing")
	}

	run := s.query.AgentRun

	query := run.WithContext(ctx).Where(run.SessionID.Eq(sessionID))
	if options.State != nil {
		query = query.Where(run.State.Eq(string(*options.State)))
	}

	page := readPage{limit: options.Limit, offset: options.Offset}

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.AgentRun, error) {
			return query.
				Order(run.StartedAt.Desc(), run.ID.Desc()).
				Offset(offset).
				Limit(limit).
				Find()
		},
		"list agent runs",
		"probe agent run page continuation",
	)
	if err != nil {
		return nil, err
	}

	return &AgentRunPage{
		Items:   items,
		Limit:   options.Limit,
		Offset:  options.Offset,
		HasMore: hasMore,
	}, nil
}

// ListAgentRunEvents returns ordered durable replay data for one child run.
func (s *Store) ListAgentRunEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	options ListAgentRunEventsOptions,
) (*AgentRunEventPage, error) {
	options, err := normalizeAgentRunEventListOptions(options)
	if err != nil {
		return nil, err
	}

	run, err := s.GetAgentRun(ctx, sessionID, agentRunID)
	if err != nil {
		return nil, err
	}

	event := s.query.AgentRunEvent
	query := event.WithContext(ctx).
		Where(
			event.SessionID.Eq(sessionID),
			event.AgentRunID.Eq(agentRunID),
			event.Sequence.Gte(options.Cursor+1),
		).
		Order(event.Sequence.Asc(), event.ID.Asc())

	items, err := query.Limit(options.Limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list agent run events")
	}

	nextCursor := options.Cursor
	if len(items) > 0 {
		nextCursor = items[len(items)-1].Sequence
	}

	probe, err := query.Offset(len(items)).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe agent run event continuation")
	}

	return &AgentRunEventPage{
		Run:        run,
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    len(probe) > 0,
	}, nil
}

// RecoverInterruptedAgentRuns marks stale child runs terminal during startup.
func (s *Store) RecoverInterruptedAgentRuns(ctx context.Context) (int, error) {
	var recovered int

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		run := tx.AgentRun

		running, err := run.WithContext(ctx).
			Where(run.State.Eq(string(models.AgentRunStateRunning))).
			Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list running agent runs")
		}

		for _, item := range running {
			if _, err := run.WithContext(ctx).
				Where(
					run.ID.Eq(item.ID),
					run.State.Eq(string(models.AgentRunStateRunning)),
				).
				UpdateSimple(
					run.State.Value(string(models.AgentRunStateInterrupted)),
					run.FailureClassification.Value("interrupted"),
					run.FailureDetail.Value(
						"process stopped before child agent completed",
					),
					run.EndedAt.Value(s.now()),
				); err != nil {
				return ctxerrors.Wrap(err, "mark agent run interrupted")
			}

			recovered++
		}

		return nil
	}); err != nil {
		return 0, ctxerrors.Wrap(err, "recover interrupted agent runs")
	}

	return recovered, nil
}

func validateAgentRunStart(
	sessionID uuid.UUID,
	input StartAgentRunInput,
) error {
	if sessionID == uuid.Nil || input.ParentTurnID == uuid.Nil ||
		input.RequestID == uuid.Nil || input.Depth < 1 {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "agent run")
	}

	if hasBlank(
		input.Name,
		input.Workspace,
		input.ModelReference,
		input.ModelID,
		input.Task,
		input.Instructions,
		input.SystemPrompt,
	) {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "agent run")
	}

	if input.Definition != models.AgentRunDefinitionStored &&
		input.Definition != models.AgentRunDefinitionAdHoc {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent run definition",
		)
	}

	if !json.Valid([]byte(input.AllowedToolsJSON)) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent allowed tools JSON",
		)
	}

	return nil
}

func hasBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}

	return false
}

func isAgentRunTerminalState(state models.AgentRunState) bool {
	return state == models.AgentRunStateCompleted ||
		state == models.AgentRunStateFailed ||
		state == models.AgentRunStateCancelled ||
		state == models.AgentRunStateInterrupted
}

func (s *Store) findAgentRunWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRun, error) {
	run, err := query.AgentRun.WithContext(ctx).
		Where(
			query.AgentRun.ID.Eq(agentRunID),
			query.AgentRun.SessionID.Eq(sessionID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query agent run")
	}

	return run, nil
}

func normalizeAgentRunListOptions(
	options ListAgentRunsOptions,
) (ListAgentRunsOptions, error) {
	if options.Limit == 0 {
		options.Limit = DefaultPageLimit
	}

	if options.Limit < 1 || options.Limit > MaximumPageLimit ||
		options.Offset < 0 {
		return ListAgentRunsOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"agent run page",
		)
	}

	if options.State != nil && !isAgentRunState(*options.State) {
		return ListAgentRunsOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"agent run state",
		)
	}

	return options, nil
}

func normalizeAgentRunEventListOptions(
	options ListAgentRunEventsOptions,
) (ListAgentRunEventsOptions, error) {
	if options.Limit == 0 {
		options.Limit = DefaultPageLimit
	}

	if options.Limit < 1 || options.Limit > MaximumPageLimit ||
		options.Cursor < 0 {
		return ListAgentRunEventsOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"agent run event page",
		)
	}

	return options, nil
}

func isAgentRunState(state models.AgentRunState) bool {
	return state == models.AgentRunStateRunning ||
		state == models.AgentRunStateCompleted ||
		state == models.AgentRunStateFailed ||
		state == models.AgentRunStateCancelled ||
		state == models.AgentRunStateInterrupted
}
