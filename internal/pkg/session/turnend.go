package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// TurnEnd is how a running turn ended when no process finalizes it through
// its lease: Stop, a failed workflow, or a process that died.
type TurnEnd struct {
	State          models.TurnState `json:"state"`
	Classification string           `json:"classification"`
	EventType      string           `json:"eventType"`
}

// turnEndPayload is the data of a turn.failed or turn.cancelled frame.
type turnEndPayload struct {
	Reason string `json:"reason"`
}

// CancelledTurnEnd is a turn Stop ended.
func CancelledTurnEnd() TurnEnd {
	return TurnEnd{
		State:          models.TurnStateCancelled,
		Classification: TurnFailureCancelled,
		EventType:      EventTypeTurnCancelled,
	}
}

// FailedTurnEnd is a turn whose workflow failed.
func FailedTurnEnd() TurnEnd {
	return TurnEnd{
		State:          models.TurnStateFailed,
		Classification: TurnFailureAgentRun,
		EventType:      EventTypeTurnFailed,
	}
}

// interruptedTurnEnd is a turn whose process died before it ended.
func interruptedTurnEnd() TurnEnd {
	return TurnEnd{
		State:          models.TurnStateInterrupted,
		Classification: TurnFailureAgentRun,
		EventType:      EventTypeTurnFailed,
	}
}

// EndRunningTurn ends a turn that is still running without holding its lease,
// and stores its terminal frame in the same feed transaction, so a client
// reading the feed sees the turn end. It reports false and changes nothing
// when the turn has already ended.
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
// time, classification, incomplete messages, and one terminal event. It runs
// inside a feed transaction.
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

	payload, err := json.Marshal(turnEndPayload{Reason: end.Classification})
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

	return true, nil
}
