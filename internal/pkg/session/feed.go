package session

import (
	"cmp"
	"context"
	"database/sql/driver"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

const (
	// MaximumFeedLimit bounds one ListFeed page across events, agent run
	// events, and notices combined.
	MaximumFeedLimit = 200

	// FeedEventTypeNoticeCreated is the live frame type a notice feed item
	// carries. A notice has no event_type column of its own, unlike the
	// event and agent run event tables it shares the feed sequence with.
	FeedEventTypeNoticeCreated = "notice.created"
)

// FeedKind identifies which durable table a FeedItem came from.
type FeedKind string

const (
	FeedKindEvent         FeedKind = "event"
	FeedKindAgentRunEvent FeedKind = "agentRunEvent"
	FeedKindNotice        FeedKind = "notice"
)

// FeedItem is one row of a session's contiguous feed sequence, merged across
// events, agent_run_events, and session_notices.
type FeedItem struct {
	SessionID uuid.UUID
	Sequence  int64
	ID        uuid.UUID
	Kind      FeedKind
	EventType string

	// RequestID is set for an event item and Nil otherwise.
	RequestID uuid.UUID
	// TurnID is set for an event item.
	TurnID uuid.UUID

	// AgentRunID, ParentTurnID, and ParentAgentRunID are set for an agent
	// run event item. ParentAgentRunID is nil for a top-level child.
	AgentRunID       uuid.UUID
	ParentTurnID     uuid.UUID
	ParentAgentRunID *uuid.UUID

	PayloadJSON string

	// Notice is set for a notice item.
	Notice *models.SessionNotice

	CreatedAt time.Time
}

// FeedPage is one page of a session's merged feed, oldest first.
type FeedPage struct {
	Items      []FeedItem
	NextCursor int64
	HasMore    bool
	Head       int64
}

// feedCollector gathers the first feed sequence allocated per session during
// one feedTransaction call, so the caller can signal each session once after
// commit.
type feedCollector struct {
	mu    sync.Mutex
	first map[uuid.UUID]int64
}

func newFeedCollector() *feedCollector {
	return &feedCollector{first: make(map[uuid.UUID]int64)}
}

// record keeps only the first allocation seen for sessionID. Later writes in
// the same transaction allocate strictly higher sequences, so the first
// recorded value is always the lowest.
func (c *feedCollector) record(sessionID uuid.UUID, first int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.first[sessionID]; exists {
		return
	}

	c.first[sessionID] = first
}

func (c *feedCollector) snapshot() map[uuid.UUID]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[uuid.UUID]int64, len(c.first))
	maps.Copy(out, c.first)

	return out
}

type feedCollectorContextKey struct{}

func contextWithFeedCollector(
	ctx context.Context,
	collector *feedCollector,
) context.Context {
	return context.WithValue(ctx, feedCollectorContextKey{}, collector)
}

func feedCollectorFromContext(ctx context.Context) *feedCollector {
	collector, _ := ctx.Value(feedCollectorContextKey{}).(*feedCollector)

	return collector
}

// FeedSignalFunc notifies that sessionID's feed gained rows starting at
// first. Store calls it at most once per (session, commit), strictly after
// the writing transaction returned nil, never on rollback.
type FeedSignalFunc func(sessionID uuid.UUID, first int64)

// SetFeedSignal installs the post-commit feed notifier, safe for concurrent
// use. A nil signal disables notification. Embedded runtimes never set one.
func (s *Store) SetFeedSignal(signal FeedSignalFunc) {
	s.feedSignalMu.Lock()
	defer s.feedSignalMu.Unlock()

	s.feedSignal = signal
}

func (s *Store) getFeedSignal() FeedSignalFunc {
	s.feedSignalMu.RLock()
	defer s.feedSignalMu.RUnlock()

	return s.feedSignal
}

// feedTransaction runs fn in one transaction and, once it commits, calls the
// installed feed signal for every session whose feed gained rows during the
// call, with the first sequence allocated for that session. fn's ctx carries
// the collector allocateFeedSequences records into, so every nested call that
// forwards ctx unchanged participates automatically.
func (s *Store) feedTransaction(
	ctx context.Context,
	fn func(ctx context.Context, tx *repositories.Query) error,
) error {
	collector := newFeedCollector()
	txCtx := contextWithFeedCollector(ctx, collector)

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		return fn(txCtx, tx)
	}); err != nil {
		return ctxerrors.Wrap(err, "run feed transaction")
	}

	signal := s.getFeedSignal()
	if signal == nil {
		return nil
	}

	for sessionID, first := range collector.snapshot() {
		signal(sessionID, first)
	}

	return nil
}

// allocateFeedSequences reserves count consecutive feed sequences for
// sessionID inside tx and returns the first one. The reservation and the
// caller's insert must share tx, so a rollback discards both together. count
// <= 0 allocates nothing and returns 0, nil without writing.
func (s *Store) allocateFeedSequences(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	count int,
) (int64, error) {
	if count <= 0 {
		return 0, nil
	}

	// A write outside feedTransaction would commit feed rows nobody signals,
	// so live clients would only see them after some later write.
	collector := feedCollectorFromContext(ctx)
	if collector == nil {
		return 0, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"feed sequence allocated outside a feed transaction",
		)
	}

	repository := tx.Session

	result, err := repository.WithContext(ctx).
		Where(repository.ID.Eq(sessionID)).
		UpdateSimple(repository.FeedSequence.Add(int64(count)))
	if err != nil {
		return 0, ctxerrors.Wrap(err, "allocate feed sequence")
	}

	if result.RowsAffected == 0 {
		return 0, ctxerrors.Wrap(
			commerr.ErrNotFound,
			"session for feed sequence allocation",
		)
	}

	session, err := repository.WithContext(ctx).
		Where(repository.ID.Eq(sessionID)).
		First()
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read allocated feed sequence")
	}

	first := session.FeedSequence - int64(count) + 1
	collector.record(sessionID, first)

	return first, nil
}

// FeedHead returns the current head of a session's feed sequence.
func (s *Store) FeedHead(
	ctx context.Context,
	sessionID uuid.UUID,
) (int64, error) {
	session, err := s.findSession(ctx, sessionID)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "get session feed head")
	}

	return session.FeedSequence, nil
}

// TurnFeedStart returns the first feed sequence an event of turnID holds.
// The second return is false when the turn has no events yet.
func (s *Store) TurnFeedStart(
	ctx context.Context,
	sessionID uuid.UUID,
	turnID uuid.UUID,
) (int64, bool, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return 0, false, ctxerrors.Wrap(err, "find session for turn feed start")
	}

	event := s.query.Event

	first, err := event.WithContext(ctx).
		Where(event.SessionID.Eq(sessionID), event.TurnID.Eq(turnID)).
		Order(event.FeedSequence.Asc()).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, ctxerrors.Wrap(err, "find turn feed start")
	}

	return first.FeedSequence, true, nil
}

// ListFeed returns a page of a session's merged feed sequence, strictly
// after cursor, oldest first. A cursor at or past the head returns an empty
// page with NextCursor equal to cursor.
func (s *Store) ListFeed(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) (*FeedPage, error) {
	cursor, limit, err := normalizeFeedPage(cursor, limit)
	if err != nil {
		return nil, err
	}

	session, err := s.findSession(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find session for feed listing")
	}

	events, err := s.listFeedEventItems(ctx, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}

	agentRunEvents, err := s.listFeedAgentRunEventItems(
		ctx, sessionID, cursor, limit,
	)
	if err != nil {
		return nil, err
	}

	notices, err := s.listFeedNoticeItems(ctx, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}

	items, hasMore := mergeFeedCandidates(
		events, agentRunEvents, notices, limit,
	)

	if err := s.attachFeedAgentRunParents(ctx, sessionID, items); err != nil {
		return nil, err
	}

	nextCursor := cursor
	if len(items) > 0 {
		nextCursor = items[len(items)-1].Sequence
	}

	return &FeedPage{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Head:       session.FeedSequence,
	}, nil
}

func normalizeFeedPage(cursor int64, limit int) (int64, int, error) {
	if cursor < 0 {
		return 0, 0, ctxerrors.Wrap(commerr.ErrValidationFailed, "feed cursor")
	}

	if limit < 1 || limit > MaximumFeedLimit {
		return 0, 0, ctxerrors.Wrap(commerr.ErrValidationFailed, "feed limit")
	}

	return cursor, limit, nil
}

func (s *Store) listFeedEventItems(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) ([]FeedItem, error) {
	event := s.query.Event

	rows, err := event.WithContext(ctx).
		Where(
			event.SessionID.Eq(sessionID),
			event.FeedSequence.Gt(cursor),
		).
		Order(event.FeedSequence.Asc()).
		Limit(limit + 1).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list feed events")
	}

	items := make([]FeedItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, FeedItem{
			SessionID:   sessionID,
			Sequence:    row.FeedSequence,
			ID:          row.ID,
			Kind:        FeedKindEvent,
			EventType:   row.EventType,
			RequestID:   row.RequestID,
			TurnID:      row.TurnID,
			PayloadJSON: row.PayloadJSON,
			CreatedAt:   row.CreatedAt,
		})
	}

	return items, nil
}

func (s *Store) listFeedAgentRunEventItems(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) ([]FeedItem, error) {
	event := s.query.AgentRunEvent

	rows, err := event.WithContext(ctx).
		Where(
			event.SessionID.Eq(sessionID),
			event.FeedSequence.Gt(cursor),
		).
		Order(event.FeedSequence.Asc()).
		Limit(limit + 1).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list feed agent run events")
	}

	items := make([]FeedItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, FeedItem{
			SessionID:   sessionID,
			Sequence:    row.FeedSequence,
			ID:          row.ID,
			Kind:        FeedKindAgentRunEvent,
			EventType:   row.EventType,
			AgentRunID:  row.AgentRunID,
			PayloadJSON: row.PayloadJSON,
			CreatedAt:   row.CreatedAt,
		})
	}

	return items, nil
}

func (s *Store) listFeedNoticeItems(
	ctx context.Context,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) ([]FeedItem, error) {
	notice := s.query.SessionNotice

	rows, err := notice.WithContext(ctx).
		Where(
			notice.SessionID.Eq(sessionID),
			notice.FeedSequence.Gt(cursor),
		).
		Order(notice.FeedSequence.Asc()).
		Limit(limit + 1).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list feed notices")
	}

	items := make([]FeedItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, FeedItem{
			SessionID: sessionID,
			Sequence:  row.FeedSequence,
			ID:        row.ID,
			Kind:      FeedKindNotice,
			EventType: FeedEventTypeNoticeCreated,
			Notice:    row,
			CreatedAt: row.CreatedAt,
		})
	}

	return items, nil
}

// mergeFeedCandidates merges three ascending, feed-sequence-ordered slices,
// each already capped at limit+1, and cuts the result to limit. Fetching
// limit+1 from every kind before merging is enough: a row ranking within the
// global top limit+1 also ranks within the top limit+1 of its own kind,
// because same-kind rows keep their relative order across the merge.
func mergeFeedCandidates(
	events, agentRunEvents, notices []FeedItem,
	limit int,
) ([]FeedItem, bool) {
	merged := make(
		[]FeedItem,
		0,
		len(events)+len(agentRunEvents)+len(notices),
	)
	merged = append(merged, events...)
	merged = append(merged, agentRunEvents...)
	merged = append(merged, notices...)

	slices.SortFunc(merged, func(a, b FeedItem) int {
		return cmp.Compare(a.Sequence, b.Sequence)
	})

	if len(merged) <= limit {
		return merged, false
	}

	return merged[:limit], true
}

// attachFeedAgentRunParents loads ParentTurnID and ParentAgentRunID for
// every agent run event item in one batched query on agent_runs.
func (s *Store) attachFeedAgentRunParents(
	ctx context.Context,
	sessionID uuid.UUID,
	items []FeedItem,
) error {
	ids := make(map[uuid.UUID]struct{})

	for _, item := range items {
		if item.Kind != FeedKindAgentRunEvent {
			continue
		}

		ids[item.AgentRunID] = struct{}{}
	}

	if len(ids) == 0 {
		return nil
	}

	valuers := make([]driver.Valuer, 0, len(ids))
	for id := range ids {
		valuers = append(valuers, id)
	}

	run := s.query.AgentRun

	runs, err := run.WithContext(ctx).
		Where(run.SessionID.Eq(sessionID), run.ID.In(valuers...)).
		Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read feed agent run parents")
	}

	parents := make(map[uuid.UUID]*models.AgentRun, len(runs))
	for _, record := range runs {
		parents[record.ID] = record
	}

	for index := range items {
		if items[index].Kind != FeedKindAgentRunEvent {
			continue
		}

		parent, ok := parents[items[index].AgentRunID]
		if !ok {
			continue
		}

		items[index].ParentTurnID = parent.ParentTurnID
		items[index].ParentAgentRunID = parent.ParentAgentRunID
	}

	return nil
}
