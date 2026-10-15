package session

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// Cancel records a stop revision even when the session has no active turn.
// Its result reports whether a turn or execution was stopped.
func (s *Store) Cancel(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	s.activeMu.Lock()
	active, held := s.active[sessionID]
	requested := false
	paused := false

	var revision int64

	err := s.query.Transaction(func(tx *repositories.Query) error {
		var err error

		revision, err = s.advanceStopRevision(ctx, tx, sessionID)
		if err != nil {
			return ctxerrors.Wrap(err, "advance session stop revision")
		}

		paused, err = s.pauseSessionExecutions(ctx, tx, sessionID)
		if err != nil {
			return ctxerrors.Wrap(err, "pause stopped session executions")
		}

		if !held {
			return nil
		}

		turn := tx.Turn

		result, err := turn.WithContext(ctx).Where(
			turn.SessionID.Eq(sessionID), turn.ID.Eq(active.turnID),
			turn.State.Eq(string(models.TurnStateRunning)),
		).UpdateSimple(turn.CancelRequested.Value(true))
		if err != nil {
			return ctxerrors.Wrap(err, "record turn cancellation")
		}

		requested = result.RowsAffected != 0

		return nil
	})
	s.activeMu.Unlock()

	if err != nil {
		return false, ctxerrors.Wrap(err, "record session stop")
	}

	if requested {
		cancel, shouldCall := s.markCancelRequested(sessionID, active.turnID)
		if shouldCall && cancel != nil {
			cancel()
		}
	}

	ctxscope.GetLogger(ctx).Info("session stop recorded",
		"session_id", sessionID, "stop_revision", revision,
		"cancel_requested", requested)

	return requested || paused, nil
}

func (s *Store) advanceStopRevision(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
) (int64, error) {
	stored, err := s.findSessionWithQuery(ctx, query, sessionID)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read stopped session")
	}

	if stored.StopRevision == math.MaxInt64 {
		return 0, ctxerrors.Wrap(commerr.ErrInvalidState,
			"session stop revision exhausted")
	}

	revision := stored.StopRevision + 1
	repository := query.Session

	_, err = repository.WithContext(ctx).Where(repository.ID.Eq(sessionID)).
		UpdateSimple(repository.StopRevision.Value(revision))
	if err != nil {
		return 0, ctxerrors.Wrap(err, "save session stop revision")
	}

	return revision, nil
}

func (s *Store) admissionStopRevision(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	expected *int64,
) (int64, error) {
	stored, err := s.findSessionWithQuery(ctx, query, sessionID)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read admission stop revision")
	}

	if err := ValidateStopRevision(expected, stored.StopRevision); err != nil {
		return 0, ctxerrors.Wrap(err, "validate admission stop revision")
	}

	return stored.StopRevision, nil
}

func (s *Store) validateDeferredStopRevision(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	expected *int64,
	message *models.ExecutionInboxMessage,
) error {
	revision, err := s.admissionStopRevision(ctx, query, sessionID, expected)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred admission revision")
	}

	if message.StopRevision != revision {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"deferred input remains stopped")
	}

	return nil
}

// ValidateStopRevision refuses requests accepted before a later stop.
// Nil adopts the current revision for callers making a fresh admission.
func ValidateStopRevision(expected *int64, current int64) error {
	if expected == nil {
		return nil
	}

	if *expected < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"negative admission stop revision")
	}

	if *expected != current {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"session stopped after request acceptance")
	}

	return nil
}
