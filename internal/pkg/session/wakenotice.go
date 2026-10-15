package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func validateSessionNoticeReadOptions(options ListSessionNoticesOptions) error {
	switch options.State {
	case "", models.NoticeStatePending, models.NoticeStateDelivered:
	default:
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"notice state filter is invalid")
	}

	if options.ValidateWakeSource &&
		(options.ID == nil || *options.ID == uuid.Nil) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"wake validation requires an exact notice identity")
	}

	return nil
}

// This read is advisory. Turn admission rechecks the same source atomically.
func (s *Store) validatePendingWakeNotice(
	ctx context.Context,
	sessionID uuid.UUID,
	notice *models.SessionNotice,
) error {
	if notice.State != models.NoticeStatePending {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"wake notice was already delivered")
	}

	return s.validateWakeSource(ctx, s.query, sessionID, notice)
}
