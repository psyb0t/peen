package session

import (
	"context"
	"database/sql/driver"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
)

// TurnEnd is how a running turn ended when no process finalizes it through
// its lease: Stop, a failed workflow, or a process that died. Detail is the
// error that ended it, as a client and the model may read it.
type TurnEnd struct {
	State          models.TurnState `json:"state"`
	Classification string           `json:"classification"`
	EventType      string           `json:"eventType"`
	Detail         string           `json:"detail,omitempty"`
}

// turnEndPayload is the data of a turn.failed or turn.cancelled frame.
type turnEndPayload struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// TurnEndNotice is the data of a turn.failed or turn.cancelled notice.
// Message is the turn's opening user message, UndeliveredMessages the
// messages queued for the turn that never reached the model, and ToolCalls
// the calls the turn made, so the next turn knows what was asked and which
// effects already happened even though the ended turn's messages are left
// out of its history.
type TurnEndNotice struct {
	TurnID              uuid.UUID         `json:"turnId"`
	State               models.TurnState  `json:"state"`
	Reason              string            `json:"reason"`
	Error               string            `json:"error,omitempty"`
	Message             string            `json:"message,omitempty"`
	UndeliveredMessages []string          `json:"undeliveredMessages,omitempty"`
	ToolCalls           []TurnEndToolCall `json:"toolCalls,omitempty"`
	ToolCallsOmitted    int               `json:"toolCallsOmitted,omitempty"`
}

// turnEndToolOutcome says how one of an ended turn's tool calls came out.
type turnEndToolOutcome = string

const (
	turnEndToolSucceeded turnEndToolOutcome = "succeeded"
	turnEndToolFailed    turnEndToolOutcome = "failed"
	// turnEndToolNoResult is a call whose result was never stored, so its
	// effect is unknown.
	turnEndToolNoResult turnEndToolOutcome = "noResult"
)

// TurnEndToolCall is one tool call an ended turn made, with its arguments
// cut to turnEndToolArgumentBytes.
type TurnEndToolCall struct {
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Outcome   turnEndToolOutcome `json:"outcome"`
}

// storedToolCall is one entry of a message's stored tool calls.
//
//nolint:tagliatelle // Stored calls keep the names Elelem's ToolCall encodes.
type storedToolCall struct {
	ID        string          `json:"ID"`
	Name      string          `json:"Name"`
	Arguments json.RawMessage `json:"Arguments"`
}

// CancelledTurnEnd is a turn Stop ended.
func CancelledTurnEnd() TurnEnd {
	return TurnEnd{
		State:          models.TurnStateCancelled,
		Classification: TurnFailureCancelled,
		EventType:      EventTypeTurnCancelled,
	}
}

// FailedTurnEnd is a turn whose workflow failed with detail.
func FailedTurnEnd(detail string) TurnEnd {
	return TurnEnd{
		State:          models.TurnStateFailed,
		Classification: TurnFailureAgentRun,
		EventType:      EventTypeTurnFailed,
		Detail:         detail,
	}
}

// interruptedTurnEnd is a turn whose process died before it ended.
func interruptedTurnEnd() TurnEnd {
	return TurnEnd{
		State:          models.TurnStateInterrupted,
		Classification: TurnFailureAgentRun,
		EventType:      EventTypeTurnFailed,
		Detail:         turnInterruptedDetail,
	}
}

// Kind is the end without its detail, which is what tells two ends apart.
func (e TurnEnd) Kind() TurnEnd {
	e.Detail = ""

	return e
}

// EndRunningTurn ends a turn that is still running without holding its lease,
// and stores its terminal frame and notice in the same feed transaction, so a
// client reading the feed sees the turn end and the next turn learns why. It
// reports false and changes nothing when the turn has already ended.
func (s *Store) EndRunningTurn(
	ctx context.Context,
	sessionID, turnID uuid.UUID,
	end TurnEnd,
) (bool, error) {
	ended := false

	err := s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		repository := tx.Turn

		turn, err := repository.WithContext(ctx).Where(
			repository.SessionID.Eq(sessionID),
			repository.ID.Eq(turnID),
		).First()
		if err != nil {
			return ctxerrors.Wrap(err, "find turn to end")
		}

		ended, err = s.endRunningTurn(ctx, tx, turn, end)

		return err
	})
	if err != nil {
		return false, ctxerrors.Wrap(err, "end running turn")
	}

	return ended, nil
}

// endRunningTurn records end on turn when it is still running: state, end
// time, classification, incomplete messages, one terminal event, and the
// notice. It runs inside a feed transaction.
func (s *Store) endRunningTurn(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
	end TurnEnd,
) (bool, error) {
	repository := tx.Turn

	result, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(turn.ID),
		repository.State.Eq(string(models.TurnStateRunning)),
	).UpdateSimple(
		repository.State.Value(string(end.State)),
		repository.CompletedAt.Value(s.now()),
		repository.FailureClassification.Value(end.Classification),
	)
	if err != nil {
		return false, ctxerrors.Wrap(err, "mark turn ended")
	}

	if result.RowsAffected == 0 {
		return false, nil
	}

	message := tx.Message
	if _, err := message.WithContext(ctx).
		Where(message.TurnID.Eq(turn.ID)).
		UpdateSimple(message.Incomplete.Value(true)); err != nil {
		return false, ctxerrors.Wrap(err, "mark ended turn messages incomplete")
	}

	detail := boundTurnEndDetail(end.Detail)

	payload, err := json.Marshal(turnEndPayload{
		Reason: end.Classification, Error: detail,
	})
	if err != nil {
		return false, ctxerrors.Wrap(err, "encode turn end frame")
	}

	if err := s.appendEvents(ctx, tx,
		Lease{SessionID: turn.SessionID, TurnID: turn.ID},
		[]EventInput{{
			RequestID:          turn.RequestID,
			WorkerGenerationID: turn.WorkerGenerationID,
			EventType:          end.EventType,
			PayloadJSON:        string(payload),
		}},
	); err != nil {
		return false, ctxerrors.Wrap(err, "store turn end frame")
	}

	if err := s.recordTurnEndNotice(ctx, tx, turn.SessionID, TurnEndNotice{
		TurnID: turn.ID, State: end.State,
		Reason: end.Classification, Error: detail,
	}); err != nil {
		return false, ctxerrors.Wrap(err, "record turn end notice")
	}

	return true, nil
}

// recordTurnEndNotice queues the notice that tells the session's clients a
// turn ended without completing, and why, and adds the system message that
// tells the agent the same in the next turn's history. It never wakes the
// session, so a failing turn cannot start another one by itself.
func (s *Store) recordTurnEndNotice(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	data TurnEndNotice,
) error {
	data.Error = boundTurnEndDetail(data.Error)

	if err := readEndedTurnMessages(ctx, tx, sessionID, &data); err != nil {
		return ctxerrors.Wrap(err, "read ended turn messages")
	}

	if err := readEndedTurnToolCalls(ctx, tx, sessionID, &data); err != nil {
		return ctxerrors.Wrap(err, "read ended turn tool calls")
	}

	if data.State == models.TurnStateCompleted {
		return nil
	}

	if err := s.saveTurnEndNotice(ctx, tx, sessionID, data); err != nil {
		return err
	}

	if err := s.appendTurnEndMessage(ctx, tx, data); err != nil {
		return ctxerrors.Wrap(err, "add turn end message")
	}

	return nil
}

func (s *Store) saveTurnEndNotice(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	data TurnEndNotice,
) error {
	noticeType := events.TypeTurnFailed
	summary := turnFailedNoticeSummary

	switch data.State {
	case models.TurnStateCancelled:
		noticeType = events.TypeTurnCancelled
		summary = turnCancelledNoticeSummary
	case models.TurnStateInterrupted:
		summary = turnInterruptedNoticeSummary
	case models.TurnStateCompleted, models.TurnStateRunning,
		models.TurnStateFailed:
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return ctxerrors.Wrap(err, "encode turn end notice")
	}

	input, err := s.normalizeSessionNoticeInput(sessionID,
		CreateSessionNoticeInput{
			Type: noticeType, Source: TurnEndNoticeSource,
			Summary: summary, DataJSON: string(encoded),
			Delivery: models.NoticeDeliveryQueue,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "validate turn end notice")
	}

	if _, err := s.createSessionNoticeWithQuery(ctx, tx, sessionID,
		input); err != nil {
		return ctxerrors.Wrap(err, "save turn end notice")
	}

	return nil
}

// readEndedTurnMessages fills in the turn's opening user message and the
// messages still queued in the turn's root inbox, each cut to
// turnEndMessageBytes.
func readEndedTurnMessages(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	data *TurnEndNotice,
) error {
	message := tx.Message

	opening, err := message.WithContext(ctx).Where(
		message.SessionID.Eq(sessionID),
		message.TurnID.Eq(data.TurnID),
		message.Role.Eq(string(models.MessageRoleUser)),
	).Order(message.Sequence).Limit(1).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn opening message")
	}

	if len(opening) > 0 {
		data.Message = events.TruncateToRuneBoundary(opening[0].Content,
			turnEndMessageBytes)
	}

	execution := tx.Execution

	roots, err := execution.WithContext(ctx).Where(
		execution.SessionID.Eq(sessionID),
		execution.RootTurnID.Eq(data.TurnID),
		execution.Kind.Eq(string(models.ExecutionKindRoot)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn roots")
	}

	if len(roots) == 0 {
		return nil
	}

	rootIDs := make([]driver.Valuer, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}

	inbox := tx.ExecutionInboxMessage

	queued, err := inbox.WithContext(ctx).Where(
		inbox.SessionID.Eq(sessionID),
		inbox.ExecutionID.In(rootIDs...),
		inbox.State.Eq(string(models.InboxMessageStateQueued)),
	).Order(inbox.Sequence).Limit(turnEndUndeliveredLimit).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn queued messages")
	}

	for _, row := range queued {
		data.UndeliveredMessages = append(data.UndeliveredMessages,
			events.TruncateToRuneBoundary(row.Message, turnEndMessageBytes))
	}

	return nil
}

// readEndedTurnToolCalls lists the tool calls the ended turn made, with how
// each came out, keeping the latest turnEndToolCallLimit.
func readEndedTurnToolCalls(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	data *TurnEndNotice,
) error {
	message := tx.Message

	rows, err := message.WithContext(ctx).Select(message.Role,
		message.ToolCallsJSON, message.ToolCallID, message.IsError,
	).Where(
		message.SessionID.Eq(sessionID),
		message.TurnID.Eq(data.TurnID),
		message.Role.In(string(models.MessageRoleAssistant),
			string(models.MessageRoleTool)),
	).Order(message.Sequence).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn tool messages")
	}

	failed := map[string]bool{}

	for _, row := range rows {
		if row.Role == models.MessageRoleTool {
			failed[row.ToolCallID] = row.IsError
		}
	}

	var calls []TurnEndToolCall

	for _, row := range rows {
		if row.Role != models.MessageRoleAssistant || row.ToolCallsJSON == "" {
			continue
		}

		var stored []storedToolCall
		if err := json.Unmarshal([]byte(row.ToolCallsJSON),
			&stored); err != nil {
			return ctxerrors.Wrap(err, "decode ended turn tool calls")
		}

		for _, call := range stored {
			calls = append(calls, endedTurnToolCall(call, failed))
		}
	}

	if len(calls) > turnEndToolCallLimit {
		data.ToolCallsOmitted = len(calls) - turnEndToolCallLimit
		calls = calls[data.ToolCallsOmitted:]
	}

	data.ToolCalls = calls

	return nil
}

func endedTurnToolCall(
	call storedToolCall,
	failed map[string]bool,
) TurnEndToolCall {
	outcome := turnEndToolNoResult

	if isError, found := failed[call.ID]; found {
		outcome = turnEndToolSucceeded
		if isError {
			outcome = turnEndToolFailed
		}
	}

	return TurnEndToolCall{
		Name: call.Name,
		Arguments: events.TruncateToRuneBoundary(string(call.Arguments),
			turnEndToolArgumentBytes),
		Outcome: outcome,
	}
}

// boundTurnEndDetail keeps a worker-supplied error within the size a frame
// and a notice carry.
func boundTurnEndDetail(detail string) string {
	return events.TruncateToRuneBoundary(detail, turnEndDetailBytes)
}
