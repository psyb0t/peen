package session

import (
	"context"
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

// turnEndNoticeData is the data of a turn.failed or turn.cancelled notice.
type turnEndNoticeData struct {
	TurnID uuid.UUID        `json:"turnId"`
	State  models.TurnState `json:"state"`
	Reason string           `json:"reason"`
	Error  string           `json:"error,omitempty"`
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

	if err := s.recordTurnEndNotice(ctx, tx, turn.SessionID, turnEndNoticeData{
		TurnID: turn.ID, State: end.State,
		Reason: end.Classification, Error: detail,
	}); err != nil {
		return false, ctxerrors.Wrap(err, "record turn end notice")
	}

	return true, nil
}

// recordTurnEndNotice queues the notice that tells the session's agents and
// clients a turn ended without completing, and why. It never wakes the
// session, so a failing turn cannot start another one by itself.
func (s *Store) recordTurnEndNotice(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	data turnEndNoticeData,
) error {
	data.Error = boundTurnEndDetail(data.Error)

	noticeType := events.TypeTurnFailed
	summary := turnFailedNoticeSummary

	switch data.State {
	case models.TurnStateCancelled:
		noticeType = events.TypeTurnCancelled
		summary = turnCancelledNoticeSummary
	case models.TurnStateInterrupted:
		summary = turnInterruptedNoticeSummary
	case models.TurnStateCompleted:
		return nil
	case models.TurnStateRunning, models.TurnStateFailed:
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

// boundTurnEndDetail keeps a worker-supplied error within the size a frame
// and a notice carry.
func boundTurnEndDetail(detail string) string {
	return events.TruncateToRuneBoundary(detail, turnEndDetailBytes)
}
