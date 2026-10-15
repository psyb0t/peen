package session

import (
	"context"
	"database/sql/driver"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const compactionAssignmentBatchSize = 256

func createCompactionForMessages(
	ctx context.Context,
	query *repositories.Query,
	compaction *models.Compaction,
	ids []uuid.UUID,
) error {
	if len(ids) == 0 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"compaction has no unassigned completed messages")
	}

	if err := query.Compaction.WithContext(ctx).Create(compaction); err != nil {
		return ctxerrors.Wrap(err, "create compaction")
	}

	repository := query.Message

	for start := 0; start < len(ids); start += compactionAssignmentBatchSize {
		end := min(start+compactionAssignmentBatchSize, len(ids))

		values := make([]driver.Valuer, 0, end-start)
		for _, id := range ids[start:end] {
			values = append(values, id)
		}

		assigned, err := repository.WithContext(ctx).Where(
			repository.SessionID.Eq(compaction.SessionID),
			repository.ID.In(values...),
			repository.CompactionID.IsNull(),
		).UpdateSimple(repository.CompactionID.Value(compaction.ID))
		if err != nil {
			return ctxerrors.Wrap(err, "assign completed message compaction")
		}

		if assigned.RowsAffected != int64(end-start) {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"compaction source assignments changed")
		}
	}

	return nil
}
