package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
)

func (s *Store) validateJobWake(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	notice *models.SessionNotice,
) error {
	var data events.JobNoticeData
	if err := json.Unmarshal([]byte(notice.DataJSON), &data); err != nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"decode job wake identity")
	}

	if data.JobID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "job wake identity")
	}

	job, err := s.findJobWithQuery(ctx, query, sessionID, data.JobID)
	if err != nil {
		return ctxerrors.Wrap(err, "read owned wake source job")
	}

	if err := s.validateWakeTurn(ctx, query, sessionID,
		job.TurnID); err != nil {
		return ctxerrors.Wrap(err, "validate job wake source turn")
	}

	if job.AgentRunID == nil {
		return nil
	}

	return s.validateAgentWakeLineage(ctx, query, sessionID, *job.AgentRunID)
}
