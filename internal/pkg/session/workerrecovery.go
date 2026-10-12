package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// RecoverWorkerTurns releases only turns owned by the worker that ended.
// Their checkpoints remain available for audit; they are not model history.
func (s *Store) RecoverWorkerTurns(
	ctx context.Context,
	sessionID, generationID uuid.UUID,
) (int, error) {
	if sessionID == uuid.Nil || generationID == uuid.Nil {
		return 0, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "worker turn recovery",
		)
	}

	var leases []Lease

	err := s.query.Transaction(func(tx *repositories.Query) error {
		repository := tx.Turn

		turns, err := repository.WithContext(ctx).Where(
			repository.SessionID.Eq(sessionID),
			repository.WorkerGenerationID.Eq(generationID.String()),
			repository.State.Eq(string(models.TurnStateRunning)),
		).Find()
		if err != nil {
			return ctxerrors.Wrap(err, "find interrupted worker turns")
		}

		for _, turn := range turns {
			if err := s.interruptTurn(ctx, tx, turn); err != nil {
				return ctxerrors.Wrap(err, "interrupt worker turn")
			}

			if err := s.interruptTurnModelRuns(
				ctx, tx, sessionID, turn.ID,
			); err != nil {
				return ctxerrors.Wrap(err, "interrupt worker turn model audit")
			}

			leases = append(leases, Lease{
				SessionID: sessionID, TurnID: turn.ID,
			})
		}

		return nil
	})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "recover worker turns")
	}

	for _, lease := range leases {
		s.ReleaseTurn(lease)
	}

	return len(leases), nil
}

func (s *Store) interruptTurnModelRuns(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, turnID uuid.UUID,
) error {
	repository := tx.ModelRun

	runs, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.TurnID.Eq(turnID),
		repository.State.Eq(string(models.ModelRunStateRunning)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "find worker turn model runs")
	}

	return s.interruptModelRunRows(ctx, tx, runs)
}

func (s *Store) interruptChildModelRuns(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, runID uuid.UUID,
) error {
	repository := tx.ModelRun

	runs, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.AgentRunID.Eq(runID),
		repository.State.Eq(string(models.ModelRunStateRunning)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "find interrupted child model runs")
	}

	return s.interruptModelRunRows(ctx, tx, runs)
}

func (s *Store) interruptModelRunRows(
	ctx context.Context,
	tx *repositories.Query,
	runs []*models.ModelRun,
) error {
	for _, run := range runs {
		if _, err := s.recoverInterruptedModelRun(ctx, tx, run); err != nil {
			return ctxerrors.Wrap(err, "interrupt model audit")
		}
	}

	return nil
}
