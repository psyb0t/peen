package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// ListTurns returns a stable newest-first page of durable session turns.
func (s *Store) ListTurns(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListTurnsOptions,
) (*TurnPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for turn listing")
	}

	turn := s.query.Turn
	query := turn.WithContext(ctx).
		Where(turn.SessionID.Eq(sessionID)).
		Order(turn.StartedAt.Desc(), turn.ID.Desc())

	items, err := query.Offset(page.offset).Limit(page.limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list session turns")
	}

	probe, err := query.Offset(page.offset + page.limit).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe session turn page")
	}

	return &TurnPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: len(probe) > 0,
	}, nil
}

// ListEvents returns a stable bounded page of durable protocol events.
func (s *Store) ListEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListEventsOptions,
) (*EventPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}
	if options.Order == "" {
		options.Order = PageOrderAscending
	}
	if _, err := normalizeListOptions(ListMessagesOptions{
		Limit:  page.limit,
		Offset: page.offset,
		Order:  options.Order,
	}); err != nil {
		return nil, err
	}
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for event listing")
	}

	event := s.query.Event
	query := event.WithContext(ctx).Where(event.SessionID.Eq(sessionID))
	if options.Order == PageOrderAscending {
		query = query.Order(event.Sequence.Asc(), event.ID.Asc())
	} else {
		query = query.Order(event.Sequence.Desc(), event.ID.Desc())
	}

	items, err := query.Offset(page.offset).Limit(page.limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list session events")
	}

	probe, err := query.Offset(page.offset + page.limit).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe session event page")
	}

	return &EventPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: len(probe) > 0,
	}, nil
}

// ListCompactions returns a stable newest-first page of durable summaries.
func (s *Store) ListCompactions(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListCompactionsOptions,
) (*CompactionPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for compaction listing")
	}

	compaction := s.query.Compaction
	query := compaction.WithContext(ctx).
		Where(compaction.SessionID.Eq(sessionID)).
		Order(compaction.CreatedAt.Desc(), compaction.ID.Desc())

	items, err := query.Offset(page.offset).Limit(page.limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list session compactions")
	}

	probe, err := query.Offset(page.offset + page.limit).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe session compaction page")
	}

	return &CompactionPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: len(probe) > 0,
	}, nil
}

// GetCompaction returns one immutable compaction record owned by a session.
func (s *Store) GetCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	compactionID uuid.UUID,
) (*models.Compaction, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for compaction")
	}

	compaction := s.query.Compaction
	stored, err := compaction.WithContext(ctx).
		Where(
			compaction.ID.Eq(compactionID),
			compaction.SessionID.Eq(sessionID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get session compaction")
	}

	return stored, nil
}

// GetContextSnapshot returns a snapshot only when this session references it.
func (s *Store) GetContextSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*models.ContextSnapshot, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for context snapshot")
	}
	if err := s.assertContextSnapshotReference(ctx, sessionID, hash); err != nil {
		return nil, err
	}

	snapshot, err := s.query.ContextSnapshot.WithContext(ctx).
		Where(s.query.ContextSnapshot.Hash.Eq(hash)).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get context snapshot")
	}

	return snapshot, nil
}

// GetPromptSnapshot returns a snapshot only when this session references it.
func (s *Store) GetPromptSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*models.PromptSnapshot, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for prompt snapshot")
	}
	if err := s.assertPromptSnapshotReference(ctx, sessionID, hash); err != nil {
		return nil, err
	}

	snapshot, err := s.query.PromptSnapshot.WithContext(ctx).
		Where(s.query.PromptSnapshot.Hash.Eq(hash)).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get prompt snapshot")
	}

	return snapshot, nil
}

type readPage struct {
	limit  int
	offset int
}

func normalizeReadPage(limit, offset int) (readPage, error) {
	options, err := normalizeListOptions(ListMessagesOptions{
		Limit:  limit,
		Offset: offset,
		Order:  PageOrderAscending,
	})
	if err != nil {
		return readPage{}, err
	}

	return readPage{limit: options.Limit, offset: options.Offset}, nil
}

func (s *Store) assertContextSnapshotReference(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) error {
	turn := s.query.Turn
	if _, err := turn.WithContext(ctx).
		Where(
			turn.SessionID.Eq(sessionID),
			turn.ContextSnapshotHash.Eq(hash),
		).
		First(); err != nil {
		return ctxerrors.Wrap(err, "find session context snapshot reference")
	}

	return nil
}

func (s *Store) assertPromptSnapshotReference(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) error {
	turn := s.query.Turn
	if _, err := turn.WithContext(ctx).
		Where(
			turn.SessionID.Eq(sessionID),
			turn.PromptSnapshotHash.Eq(hash),
		).
		First(); err != nil {
		return ctxerrors.Wrap(err, "find session prompt snapshot reference")
	}

	return nil
}
