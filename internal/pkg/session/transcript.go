package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

const (
	defaultToolCallsJSON = "[]"

	// defaultMaxStoredMessageBytes bounds one message row when the caller did
	// not set a bound of its own.
	defaultMaxStoredMessageBytes = 1 << 20
)

// AppendCheckpoint records visible messages and events without ending a turn.
func (s *Store) AppendCheckpoint(
	ctx context.Context,
	lease Lease,
	messages []MessageInput,
	events []EventInput,
) error {
	if !s.hasLease(lease) {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "inactive turn lease")
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		_, err := s.appendTranscript(ctx, tx, lease, messages, events)

		return err
	}); err != nil {
		return ctxerrors.Wrap(err, "append turn checkpoint transaction")
	}

	return nil
}

// FinalizeTurn atomically writes terminal transcript data and state.
func (s *Store) FinalizeTurn(
	ctx context.Context,
	lease Lease,
	input FinalizeTurnInput,
) error {
	if !isTerminalState(input.State) {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"invalid terminal state %q",
			input.State,
		)
	}

	if !s.hasLease(lease) {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "inactive turn lease")
	}
	defer s.ReleaseTurn(lease)

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		appendResult, err := s.appendTranscript(
			ctx,
			tx,
			lease,
			input.Messages,
			input.Events,
		)
		if err != nil {
			return err
		}

		if err := s.finishTurn(ctx, tx, lease, input); err != nil {
			return err
		}

		if err := s.markUnfinishedMessages(ctx, tx, lease, input); err != nil {
			return err
		}

		return s.updateSessionForCompletion(
			ctx,
			tx,
			lease.SessionID,
			input.State,
			appendResult,
		)
	}); err != nil {
		return ctxerrors.Wrap(err, "finalize turn transaction")
	}

	return nil
}

// SaveContextSnapshot stores an immutable resolved harness context.
func (s *Store) SaveContextSnapshot(
	ctx context.Context,
	snapshot *models.ContextSnapshot,
) error {
	if snapshot == nil || snapshot.Hash == "" {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"context snapshot hash",
		)
	}

	if !json.Valid([]byte(snapshot.ManifestJSON)) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"invalid context snapshot manifest JSON",
		)
	}

	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = s.now()
	}

	if err := s.query.ContextSnapshot.WithContext(ctx).
		Create(snapshot); err != nil {
		if isDuplicateKey(err) {
			return nil
		}

		return ctxerrors.Wrap(err, "save context snapshot")
	}

	return nil
}

// SavePromptSnapshot stores an immutable effective prompt.
func (s *Store) SavePromptSnapshot(
	ctx context.Context,
	snapshot *models.PromptSnapshot,
) error {
	if snapshot == nil || snapshot.Hash == "" {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"prompt snapshot hash",
		)
	}

	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = s.now()
	}

	if err := s.query.PromptSnapshot.WithContext(ctx).
		Create(snapshot); err != nil {
		if isDuplicateKey(err) {
			return nil
		}

		return ctxerrors.Wrap(err, "save prompt snapshot")
	}

	return nil
}

// ListMessages returns a stable bounded page of one session's transcript.
func (s *Store) ListMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListMessagesOptions,
) (*MessagePage, error) {
	options, err := normalizeListOptions(options)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for message listing")
	}

	message := s.query.Message

	query := message.WithContext(ctx).Where(message.SessionID.Eq(sessionID))
	if options.Order == PageOrderAscending {
		query = query.Order(message.Sequence.Asc(), message.ID.Asc())
	} else {
		query = query.Order(message.Sequence.Desc(), message.ID.Desc())
	}

	items, err := query.Offset(options.Offset).Limit(options.Limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list transcript page")
	}

	probe, err := query.Offset(options.Offset + options.Limit).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe transcript page continuation")
	}

	return &MessagePage{
		Items:   items,
		Limit:   options.Limit,
		Offset:  options.Offset,
		HasMore: len(probe) > 0,
	}, nil
}

// CreateCompaction stores an immutable completed-history replacement summary.
func (s *Store) CreateCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	input CompactionInput,
) (*models.Compaction, error) {
	if input.FromSequence <= 0 || input.ToSequence < input.FromSequence {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"invalid compaction sequence bounds",
		)
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for compaction")
	}

	if input.ID == uuid.Nil {
		input.ID = s.newID()
	}

	compaction := &models.Compaction{
		ID:                     input.ID,
		SessionID:              sessionID,
		FromMessageID:          input.FromMessageID,
		ToMessageID:            input.ToMessageID,
		FromSequence:           input.FromSequence,
		ToSequence:             input.ToSequence,
		Summary:                input.Summary,
		SourceMessageCount:     input.SourceMessageCount,
		InputTokenCount:        input.InputTokenCount,
		SummaryTokenCount:      input.SummaryTokenCount,
		ModelID:                input.ModelID,
		PromptHash:             input.PromptHash,
		CreatedAt:              s.now(),
		SupersedesCompactionID: input.SupersedesCompactionID,
	}
	if err := s.query.Compaction.WithContext(ctx).
		Create(compaction); err != nil {
		return nil, ctxerrors.Wrap(err, "create compaction")
	}

	return compaction, nil
}

// LatestCompaction returns the current chain head, not a superseded row.
func (s *Store) LatestCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Compaction, error) {
	compaction, err := s.query.Compaction.WithContext(ctx).ActiveHead(sessionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		//nolint:nilnil // A nil compaction denotes normal summary absence.
		return nil, nil
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "get current compaction")
	}

	return compaction, nil
}

// CompletedHistory returns the current summary and the raw completed tail.
func (s *Store) CompletedHistory(
	ctx context.Context,
	sessionID uuid.UUID,
) (*History, error) {
	compaction, err := s.LatestCompaction(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get latest compaction")
	}

	var afterSequence int64
	if compaction != nil {
		afterSequence = compaction.ToSequence
	}

	messages, err := s.query.Message.WithContext(ctx).
		CompletedHistory(sessionID, afterSequence)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get completed message history")
	}

	return &History{Compaction: compaction, Messages: messages}, nil
}

// RecoverInterrupted marks active turns and their messages incomplete.
func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	var recoveredLeases []Lease

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		turn := tx.Turn

		running, err := turn.WithContext(ctx).
			Where(turn.State.Eq(string(models.TurnStateRunning))).
			Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list running turns")
		}

		for _, runningTurn := range running {
			if err := s.interruptTurn(ctx, tx, runningTurn); err != nil {
				return err
			}

			recoveredLeases = append(recoveredLeases, Lease{
				SessionID: runningTurn.SessionID,
				TurnID:    runningTurn.ID,
			})
		}

		return nil
	}); err != nil {
		return 0, ctxerrors.Wrap(err, "recover interrupted turns")
	}

	for _, lease := range recoveredLeases {
		s.ReleaseTurn(lease)
	}

	return len(recoveredLeases), nil
}

type appendResult struct {
	messageCount        int64
	lastMessageSequence int64
}

func (s *Store) appendTranscript(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	messageInputs []MessageInput,
	eventInputs []EventInput,
) (appendResult, error) {
	session, err := s.findSessionWithQuery(ctx, tx, lease.SessionID)
	if err != nil {
		return appendResult{}, err
	}

	turn, err := s.findRunningTurn(ctx, tx, lease)
	if err != nil {
		return appendResult{}, err
	}

	result, err := s.appendMessages(
		ctx,
		tx,
		lease,
		session.MessageCount,
		turn.Workspace,
		messageInputs,
	)
	if err != nil {
		return appendResult{}, err
	}

	if err := s.appendEvents(ctx, tx, lease, eventInputs); err != nil {
		return appendResult{}, err
	}

	if result.messageCount == 0 {
		return result, nil
	}

	repository := tx.Session

	now := s.now()
	if _, err := repository.WithContext(ctx).
		Where(repository.ID.Eq(lease.SessionID)).
		UpdateSimple(
			repository.MessageCount.Add(result.messageCount),
			repository.LastMessageAt.Value(now),
			repository.UpdatedAt.Value(now),
		); err != nil {
		return appendResult{}, ctxerrors.Wrap(
			err,
			"update session transcript metadata",
		)
	}

	return result, nil
}

func (s *Store) appendMessages(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	currentCount int64,
	defaultWorkspace string,
	inputs []MessageInput,
) (appendResult, error) {
	if len(inputs) == 0 {
		return appendResult{}, nil
	}

	messages := make([]*models.Message, 0, len(inputs))
	for index, input := range inputs {
		message, err := s.newMessage(
			lease,
			currentCount+int64(index)+1,
			defaultWorkspace,
			input,
		)
		if err != nil {
			return appendResult{}, err
		}

		messages = append(messages, message)
	}

	if err := tx.Message.WithContext(ctx).
		CreateInBatches(messages, len(messages)); err != nil {
		return appendResult{}, ctxerrors.Wrap(err, "append transcript messages")
	}

	return appendResult{
		messageCount:        int64(len(messages)),
		lastMessageSequence: messages[len(messages)-1].Sequence,
	}, nil
}

func (s *Store) newMessage(
	lease Lease,
	sequence int64,
	defaultWorkspace string,
	input MessageInput,
) (*models.Message, error) {
	if !isMessageRole(input.Role) {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"invalid message role %q",
			input.Role,
		)
	}

	if input.ToolCallsJSON == "" {
		input.ToolCallsJSON = defaultToolCallsJSON
	}

	if !json.Valid([]byte(input.ToolCallsJSON)) {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"invalid message tool calls JSON",
		)
	}

	if input.Workspace == "" {
		input.Workspace = defaultWorkspace
	}

	if len(input.Content) > s.maxStoredMessageBytes {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"message content is %d bytes, over the %d byte limit",
			len(input.Content),
			s.maxStoredMessageBytes,
		)
	}

	messageID := input.ID
	if messageID == uuid.Nil {
		messageID = s.newID()
	}

	return &models.Message{
		ID:            messageID,
		SessionID:     lease.SessionID,
		TurnID:        lease.TurnID,
		Sequence:      sequence,
		Workspace:     input.Workspace,
		Role:          input.Role,
		Content:       input.Content,
		ModelID:       input.ModelID,
		Thinking:      input.Thinking,
		ToolCallsJSON: input.ToolCallsJSON,
		ToolCallID:    input.ToolCallID,
		IsError:       input.IsError,
		Incomplete:    input.Incomplete,
		CreatedAt:     s.now(),
	}, nil
}

func (s *Store) appendEvents(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	inputs []EventInput,
) error {
	if len(inputs) == 0 {
		return nil
	}

	event := tx.Event
	latest, err := event.WithContext(ctx).
		Where(event.SessionID.Eq(lease.SessionID)).
		Order(event.Sequence.Desc(), event.ID.Desc()).
		First()

	var currentSequence int64
	if err == nil {
		currentSequence = latest.Sequence
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ctxerrors.Wrap(err, "get latest event sequence")
	}

	events := make([]*models.Event, 0, len(inputs))
	for index, input := range inputs {
		if !json.Valid([]byte(input.PayloadJSON)) {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"invalid event payload JSON",
			)
		}

		eventID := input.ID
		if eventID == uuid.Nil {
			eventID = s.newID()
		}

		events = append(events, &models.Event{
			ID:               eventID,
			SessionID:        lease.SessionID,
			TurnID:           lease.TurnID,
			Sequence:         currentSequence + int64(index) + 1,
			RequestID:        input.RequestID,
			EventType:        input.EventType,
			PayloadJSON:      input.PayloadJSON,
			ParentToolCallID: input.ParentToolCallID,
			CreatedAt:        s.now(),
		})
	}

	if err := event.WithContext(ctx).
		CreateInBatches(events, len(events)); err != nil {
		return ctxerrors.Wrap(err, "append transcript events")
	}

	return nil
}

func (s *Store) finishTurn(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	input FinalizeTurnInput,
) error {
	turn := tx.Turn

	assignments := []field.AssignExpr{
		turn.State.Value(string(input.State)),
		turn.CompletedAt.Value(s.now()),
		turn.FailureClassification.Value(input.FailureClassification),
	}
	if input.ContextSnapshotHash != nil {
		assignments = append(
			assignments,
			turn.ContextSnapshotHash.Value(*input.ContextSnapshotHash),
		)
	}

	if input.PromptSnapshotHash != nil {
		assignments = append(
			assignments,
			turn.PromptSnapshotHash.Value(*input.PromptSnapshotHash),
		)
	}

	result, err := turn.WithContext(ctx).
		Where(
			turn.ID.Eq(lease.TurnID),
			turn.SessionID.Eq(lease.SessionID),
			turn.State.Eq(string(models.TurnStateRunning)),
		).
		UpdateSimple(assignments...)
	if err != nil {
		return ctxerrors.Wrap(err, "finish turn")
	}

	if result.RowsAffected != 1 {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"turn is no longer running",
		)
	}

	return nil
}

func (s *Store) updateSessionForCompletion(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	state models.TurnState,
	appendResult appendResult,
) error {
	if state != models.TurnStateCompleted {
		return nil
	}

	session := tx.Session

	assignments := []field.AssignExpr{
		session.CompletedTurnCount.Add(1),
		session.UpdatedAt.Value(s.now()),
	}
	if appendResult.lastMessageSequence > 0 {
		assignments = append(
			assignments,
			session.LastCompletedSequence.Value(
				appendResult.lastMessageSequence,
			),
		)
	}

	if _, err := session.WithContext(ctx).
		Where(session.ID.Eq(sessionID)).
		UpdateSimple(assignments...); err != nil {
		return ctxerrors.Wrap(err, "update completed turn metadata")
	}

	return nil
}

// markUnfinishedMessages flags what a turn produced but did not finish.
//
// The caller can only mark the messages it is writing right now, and a turn
// that checkpointed before failing already has earlier messages on disk. Those
// belong to the same unfinished turn and must read as incomplete too.
//
// User messages are excluded. The turn was cut off while the model was
// answering; the question itself arrived whole, and flagging it would claim a
// truncation that never happened.
func (s *Store) markUnfinishedMessages(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	input FinalizeTurnInput,
) error {
	if input.State == models.TurnStateCompleted {
		return nil
	}

	message := tx.Message
	if _, err := message.WithContext(ctx).
		Where(
			message.TurnID.Eq(lease.TurnID),
			message.Role.Neq(string(models.MessageRoleUser)),
		).
		UpdateSimple(message.Incomplete.Value(true)); err != nil {
		return ctxerrors.Wrap(err, "mark unfinished turn messages incomplete")
	}

	return nil
}

func (s *Store) interruptTurn(
	ctx context.Context,
	tx *repositories.Query,
	runningTurn *models.Turn,
) error {
	turn := tx.Turn
	if _, err := turn.WithContext(ctx).
		Where(turn.ID.Eq(runningTurn.ID)).
		UpdateSimple(
			turn.State.Value(string(models.TurnStateInterrupted)),
			turn.CompletedAt.Value(s.now()),
		); err != nil {
		return ctxerrors.Wrap(err, "mark turn interrupted")
	}

	message := tx.Message
	if _, err := message.WithContext(ctx).
		Where(message.TurnID.Eq(runningTurn.ID)).
		UpdateSimple(message.Incomplete.Value(true)); err != nil {
		return ctxerrors.Wrap(err, "mark interrupted turn messages incomplete")
	}

	return nil
}

func (s *Store) findSessionWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
) (*models.Session, error) {
	session := query.Session

	result, err := session.WithContext(ctx).
		Where(session.ID.Eq(sessionID)).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "session")
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "query session")
	}

	return result, nil
}

func (s *Store) findRunningTurn(
	ctx context.Context,
	query *repositories.Query,
	lease Lease,
) (*models.Turn, error) {
	turn := query.Turn

	result, err := turn.WithContext(ctx).
		Where(
			turn.ID.Eq(lease.TurnID),
			turn.SessionID.Eq(lease.SessionID),
			turn.State.Eq(string(models.TurnStateRunning)),
		).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"turn is not running",
		)
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "query running turn")
	}

	return result, nil
}

func normalizeListOptions(
	options ListMessagesOptions,
) (ListMessagesOptions, error) {
	if options.Limit == 0 {
		options.Limit = DefaultPageLimit
	}

	if options.Order == "" {
		options.Order = PageOrderAscending
	}

	if options.Limit < 1 ||
		options.Limit > MaximumPageLimit ||
		options.Offset < 0 {
		return ListMessagesOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"limit or offset",
		)
	}

	if options.Order != PageOrderAscending &&
		options.Order != PageOrderDescending {
		return ListMessagesOptions{}, ctxerrors.Wrap(ErrInvalidPage, "order")
	}

	return options, nil
}

func isMessageRole(role models.MessageRole) bool {
	switch role {
	case models.MessageRoleUser,
		models.MessageRoleAssistant,
		models.MessageRoleTool:
		return true
	}

	return false
}

func isTerminalState(state models.TurnState) bool {
	switch state {
	case models.TurnStateCompleted,
		models.TurnStateFailed,
		models.TurnStateCancelled,
		models.TurnStateInterrupted:
		return true
	case models.TurnStateRunning:
		return false
	}

	return false
}

func (s *Store) hasLease(lease Lease) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, ok := s.active[lease.SessionID]

	return ok && active.turnID == lease.TurnID
}

func isDuplicateKey(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey)
}
