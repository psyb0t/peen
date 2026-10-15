package session

import (
	"cmp"
	"context"
	"database/sql/driver"
	"slices"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) deliverOpeningNotices(
	ctx context.Context,
	query *repositories.Query,
	lease Lease,
	input StartTurnInput,
) ([]*models.SessionNotice, error) {
	if len(input.NoticeIDs) == 0 && input.WakeNoticeID == nil {
		return nil, nil
	}

	notices, err := pendingOpeningNotices(ctx, query, lease.SessionID,
		input.NoticeIDs)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "select opening notice snapshot")
	}

	if err := s.validateWakeAdmission(ctx, query, lease.SessionID,
		input.WakeNoticeID, notices); err != nil {
		return nil, ctxerrors.Wrap(err, "validate opening wake source")
	}

	slices.SortFunc(notices, func(a, b *models.SessionNotice) int {
		return cmp.Compare(a.Sequence, b.Sequence)
	})

	if err := s.deliverPendingNotices(ctx, query, lease.SessionID,
		notices); err != nil {
		return nil, ctxerrors.Wrap(err, "consume admitted opening notices")
	}

	if err := recordTurnOpeningNotices(ctx, query, lease, notices); err != nil {
		return nil, ctxerrors.Wrap(err, "retain admitted opening notices")
	}

	return notices, nil
}

func pendingOpeningNotices(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	ids []uuid.UUID,
) ([]*models.SessionNotice, error) {
	selected := make(map[uuid.UUID]struct{}, len(ids))
	notices := make([]*models.SessionNotice, 0, len(ids))
	repository := query.SessionNotice

	for _, id := range ids {
		if id == uuid.Nil {
			return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
				"opening notice identity")
		}

		if _, duplicate := selected[id]; duplicate {
			return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"opening notice identities repeat")
		}

		selected[id] = struct{}{}

		notice, err := repository.WithContext(ctx).Where(
			repository.ID.Eq(id),
			repository.SessionID.Eq(sessionID),
		).First()
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read owned opening notice")
		}

		if notice.State != models.NoticeStatePending {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"opening notice has already been consumed")
		}

		notices = append(notices, notice)
	}

	return notices, nil
}

func recordTurnOpeningNotices(
	ctx context.Context,
	query *repositories.Query,
	lease Lease,
	notices []*models.SessionNotice,
) error {
	links := make([]*models.TurnOpeningNotice, 0, len(notices))
	for _, notice := range notices {
		links = append(links, &models.TurnOpeningNotice{
			SessionID: lease.SessionID, TurnID: lease.TurnID,
			NoticeID: notice.ID, SourceSequence: notice.Sequence,
		})
	}

	if err := query.TurnOpeningNotice.WithContext(ctx).
		CreateInBatches(links, DefaultPageLimit); err != nil {
		return ctxerrors.Wrap(err, "record turn opening notice identities")
	}

	return nil
}

func readTurnOpeningNotices(
	ctx context.Context,
	query *repositories.Query,
	lease Lease,
) ([]*models.SessionNotice, error) {
	repository := query.TurnOpeningNotice

	links, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(lease.SessionID),
		repository.TurnID.Eq(lease.TurnID),
	).Order(repository.SourceSequence).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read turn opening notice identities")
	}

	if len(links) == 0 {
		return nil, nil
	}

	notices := make([]*models.SessionNotice, 0, len(links))
	for start := 0; start < len(links); start += DefaultPageLimit {
		end := min(start+DefaultPageLimit, len(links))

		page, err := readTurnOpeningNoticePage(ctx, query, lease,
			links[start:end])
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read retained opening notice page")
		}

		notices = append(notices, page...)
	}

	return notices, nil
}

func readTurnOpeningNoticePage(
	ctx context.Context,
	query *repositories.Query,
	lease Lease,
	links []*models.TurnOpeningNotice,
) ([]*models.SessionNotice, error) {
	ids := make([]driver.Valuer, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.NoticeID)
	}

	repository := query.SessionNotice

	notices, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(lease.SessionID), repository.ID.In(ids...),
	).Order(repository.Sequence).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read owned opening notice records")
	}

	if len(notices) != len(links) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"retained opening notice records are missing")
	}

	for index, notice := range notices {
		if notice.ID != links[index].NoticeID ||
			notice.Sequence != links[index].SourceSequence ||
			notice.State != models.NoticeStateDelivered {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"retained opening notice differs from admission")
		}
	}

	return notices, nil
}
