// Package session owns Peen's durable transport-neutral session transcript.
package session

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// Store persists sessions and protects each active session turn in-process.
type Store struct {
	query                 *repositories.Query
	clock                 func() time.Time
	newID                 func() uuid.UUID
	maxStoredMessageBytes int

	activeMu sync.Mutex
	active   map[uuid.UUID]activeTurn
}

// NewStore builds a store over an already-open migrated database handle.
func NewStore(handle *db.Handle, options Options) (*Store, error) {
	if handle == nil || handle.GormDB == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"database handle",
		)
	}

	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}

	newID := options.NewID
	if newID == nil {
		newID = uuid.New
	}

	maxStoredMessageBytes := options.MaxStoredMessageBytes
	if maxStoredMessageBytes <= 0 {
		maxStoredMessageBytes = defaultMaxStoredMessageBytes
	}

	return &Store{
		query:                 repositories.Use(handle.GormDB),
		clock:                 clock,
		newID:                 newID,
		maxStoredMessageBytes: maxStoredMessageBytes,
		active:                make(map[uuid.UUID]activeTurn),
	}, nil
}

// CreateOrResume creates a generated-ID session or returns a requested one.
func (s *Store) CreateOrResume(
	ctx context.Context,
	requestedSessionID *uuid.UUID,
	options OpenSessionOptions,
) (*OpenSessionResult, error) {
	if requestedSessionID != nil {
		session, err := s.findSession(ctx, *requestedSessionID)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "resume session")
		}

		return &OpenSessionResult{Session: session}, nil
	}

	now := s.now()

	session := &models.Session{
		ID:        s.newID(),
		CreatedAt: now,
		UpdatedAt: now,
		RootAgent: options.RootAgent,
		ModelID:   options.ModelID,
	}
	if err := s.query.Session.WithContext(ctx).Create(session); err != nil {
		return nil, ctxerrors.Wrap(err, "create session")
	}

	return &OpenSessionResult{Session: session, Created: true}, nil
}

// Get returns durable metadata for one existing session.
func (s *Store) Get(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Session, error) {
	session, err := s.findSession(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get session")
	}

	return session, nil
}

// IsActive reports whether the process currently owns a running turn.
func (s *Store) IsActive(sessionID uuid.UUID) bool {
	_, active := s.activeTurn(sessionID)

	return active
}

// AcquireTurn starts one durable turn and records its initial transcript data.
func (s *Store) AcquireTurn(
	ctx context.Context,
	sessionID uuid.UUID,
	input StartTurnInput,
) (Lease, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return Lease{}, ctxerrors.Wrap(err, "find session for turn")
	}

	lease := Lease{SessionID: sessionID, TurnID: s.newID()}
	if err := s.reserveLease(lease); err != nil {
		return Lease{}, err
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		turn := &models.Turn{
			ID:        lease.TurnID,
			SessionID: lease.SessionID,
			RequestID: input.RequestID,
			Workspace: input.Workspace,
			State:     models.TurnStateRunning,
			StartedAt: s.now(),
		}
		if err := tx.Turn.WithContext(ctx).Create(turn); err != nil {
			return ctxerrors.Wrap(err, "create running turn")
		}

		_, err := s.appendTranscript(
			ctx,
			tx,
			lease,
			input.Messages,
			input.Events,
		)

		return err
	}); err != nil {
		s.ReleaseTurn(lease)

		return Lease{}, ctxerrors.Wrap(err, "start turn transaction")
	}

	return lease, nil
}

// RegisterCancellation associates cancellation with one exact active lease.
func (s *Store) RegisterCancellation(lease Lease, cancel context.CancelFunc) {
	s.activeMu.Lock()

	active, ok := s.active[lease.SessionID]
	if !ok || active.turnID != lease.TurnID {
		s.activeMu.Unlock()

		return
	}

	active.cancel = cancel
	s.active[lease.SessionID] = active
	s.activeMu.Unlock()

	if active.cancelRequested && cancel != nil {
		cancel()
	}
}

// Cancel requests cancellation for the active turn without waiting for it.
func (s *Store) Cancel(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return false, ctxerrors.Wrap(err, "find session for cancellation")
	}

	active, ok := s.activeTurn(sessionID)
	if !ok {
		return false, nil
	}

	turn := s.query.Turn

	result, err := turn.WithContext(ctx).
		Where(
			turn.ID.Eq(active.turnID),
			turn.SessionID.Eq(sessionID),
			turn.State.Eq(string(models.TurnStateRunning)),
		).
		UpdateSimple(turn.CancelRequested.Value(true))
	if err != nil {
		return false, ctxerrors.Wrap(err, "record cancellation request")
	}

	if result.RowsAffected == 0 {
		return false, nil
	}

	cancel, shouldCall := s.markCancelRequested(sessionID, active.turnID)
	if shouldCall && cancel != nil {
		cancel()
	}

	return true, nil
}

// ReleaseTurn removes a lease only when it is still the registered turn.
func (s *Store) ReleaseTurn(lease Lease) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, ok := s.active[lease.SessionID]
	if !ok || active.turnID != lease.TurnID {
		return
	}

	delete(s.active, lease.SessionID)
}

func (s *Store) findSession(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Session, error) {
	session := s.query.Session

	result, err := session.WithContext(ctx).
		Where(session.ID.Eq(sessionID)).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query session")
	}

	return result, nil
}

func (s *Store) reserveLease(lease Lease) error {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	if _, exists := s.active[lease.SessionID]; exists {
		return ctxerrors.Wrap(ErrSessionBusy, "session turn lease")
	}

	s.active[lease.SessionID] = activeTurn{turnID: lease.TurnID}

	return nil
}

func (s *Store) activeTurn(sessionID uuid.UUID) (activeTurn, bool) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, ok := s.active[sessionID]

	return active, ok
}

func (s *Store) markCancelRequested(
	sessionID uuid.UUID,
	turnID uuid.UUID,
) (context.CancelFunc, bool) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, ok := s.active[sessionID]
	if !ok || active.turnID != turnID {
		return nil, false
	}

	if active.cancelRequested {
		return nil, false
	}

	active.cancelRequested = true
	s.active[sessionID] = active

	return active.cancel, true
}

func (s *Store) now() time.Time {
	return s.clock().UTC()
}
