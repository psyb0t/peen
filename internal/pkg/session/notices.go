package session

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const defaultSessionNoticeDataJSON = "{}"

// CreateSessionNotice writes a notice before it reaches an in-process bus or
// a model. It is the durable source of the session's outside event history.
func (s *Store) CreateSessionNotice(
	ctx context.Context,
	sessionID uuid.UUID,
	input CreateSessionNoticeInput,
) (*models.SessionNotice, error) {
	input, err := s.normalizeSessionNoticeInput(sessionID, input)
	if err != nil {
		return nil, err
	}

	var result *models.SessionNotice

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findSessionWithQuery(ctx, tx, sessionID); err != nil {
			return err
		}

		notice := tx.SessionNotice

		sequence, err := nextSessionNoticeSequence(ctx, tx, sessionID)
		if err != nil {
			return ctxerrors.Wrap(err, "find latest session notice")
		}

		result = &models.SessionNotice{
			ID:        input.ID,
			SessionID: sessionID,
			Sequence:  sequence,
			Type:      input.Type,
			Source:    input.Source,
			Summary:   input.Summary,
			DataJSON:  input.DataJSON,
			Delivery:  input.Delivery,
			State:     models.NoticeStatePending,
			CreatedAt: input.CreatedAt,
		}
		if err := notice.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create session notice")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "create durable session notice")
	}

	return result, nil
}

func (s *Store) normalizeSessionNoticeInput(
	sessionID uuid.UUID,
	input CreateSessionNoticeInput,
) (CreateSessionNoticeInput, error) {
	if err := validateSessionNoticeInput(sessionID, input); err != nil {
		return CreateSessionNoticeInput{}, err
	}

	if input.DataJSON == "" {
		input.DataJSON = defaultSessionNoticeDataJSON
	}

	if input.ID == uuid.Nil {
		input.ID = s.newID()
	}

	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	} else {
		input.CreatedAt = input.CreatedAt.UTC()
	}

	return input, nil
}

func nextSessionNoticeSequence(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
) (int64, error) {
	notice := query.SessionNotice

	return nextSequence(func() (int64, error) {
		latest, err := notice.WithContext(ctx).
			Where(notice.SessionID.Eq(sessionID)).
			Order(notice.Sequence.Desc(), notice.ID.Desc()).
			First()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "query latest session notice")
		}

		return latest.Sequence, nil
	})
}

// ListSessionNotices returns all notice history, pending and delivered, in
// durable sequence order.
//
//nolint:dupl // The generated SessionNotice query has a distinct typed builder.
func (s *Store) ListSessionNotices(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListSessionNoticesOptions,
) (*NoticePage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for notice listing")
	}

	notice := s.query.SessionNotice
	query := notice.WithContext(ctx).
		Where(notice.SessionID.Eq(sessionID)).
		Order(notice.Sequence.Asc(), notice.ID.Asc())

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.SessionNotice, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list session notices",
		"probe session notice page",
	)
	if err != nil {
		return nil, err
	}

	return &NoticePage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// DrainSessionNotices marks every pending notice delivered and returns the
// exact immutable records for the next model context.
func (s *Store) DrainSessionNotices(
	ctx context.Context,
	sessionID uuid.UUID,
) ([]*models.SessionNotice, error) {
	var notices []*models.SessionNotice

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findSessionWithQuery(ctx, tx, sessionID); err != nil {
			return err
		}

		repository := tx.SessionNotice

		pending, err := repository.WithContext(ctx).
			Where(
				repository.SessionID.Eq(sessionID),
				repository.State.Eq(string(models.NoticeStatePending)),
			).
			Order(repository.Sequence.Asc(), repository.ID.Asc()).
			Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list pending session notices")
		}

		if err := s.deliverPendingNotices(
			ctx,
			tx,
			sessionID,
			pending,
		); err != nil {
			return err
		}

		notices = pending

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "drain durable session notices")
	}

	return notices, nil
}

func (s *Store) deliverPendingNotices(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	pending []*models.SessionNotice,
) error {
	repository := query.SessionNotice

	now := s.now()
	for _, notice := range pending {
		result, err := repository.WithContext(ctx).
			Where(
				repository.ID.Eq(notice.ID),
				repository.SessionID.Eq(sessionID),
				repository.State.Eq(string(models.NoticeStatePending)),
			).
			UpdateSimple(
				repository.State.Value(string(models.NoticeStateDelivered)),
				repository.DeliveredAt.Value(now),
			)
		if err != nil {
			return ctxerrors.Wrap(err, "mark session notice delivered")
		}

		if result.RowsAffected != 1 {
			return ctxerrors.Wrap(
				commerr.ErrInvalidState,
				"session notice delivery",
			)
		}

		notice.State = models.NoticeStateDelivered
		notice.DeliveredAt = &now
	}

	return nil
}

func validateSessionNoticeInput(
	sessionID uuid.UUID,
	input CreateSessionNoticeInput,
) error {
	if sessionID == uuid.Nil || strings.TrimSpace(input.Type) == "" ||
		strings.TrimSpace(input.Source) == "" ||
		strings.TrimSpace(input.Summary) == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "session notice")
	}

	if input.Delivery != models.NoticeDeliveryQueue &&
		input.Delivery != models.NoticeDeliveryWake {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"session notice delivery",
		)
	}

	if input.DataJSON != "" && !json.Valid([]byte(input.DataJSON)) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"session notice data JSON",
		)
	}

	return nil
}
