package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) validateJobParents(
	ctx context.Context,
	query *repositories.Query,
	job *models.Job,
) error {
	if _, err := s.findSessionWithQuery(ctx, query, job.SessionID); err != nil {
		return ctxerrors.Wrap(err, "read job session")
	}

	turn := query.Turn
	if _, err := turn.WithContext(ctx).Where(
		turn.SessionID.Eq(job.SessionID), turn.ID.Eq(job.TurnID),
	).First(); err != nil {
		return ctxerrors.Wrap(err, "read owned job turn")
	}

	if job.AgentRunID == nil {
		return nil
	}

	if *job.AgentRunID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "job owner identity")
	}

	run, err := s.findAgentRunWithQuery(ctx, query, job.SessionID,
		*job.AgentRunID)
	if err != nil {
		return ctxerrors.Wrap(err, "read owned job agent run")
	}

	if run.ParentTurnID != job.TurnID {
		return ctxerrors.Wrap(commerr.ErrConflict, "job owner turn differs")
	}

	return nil
}
