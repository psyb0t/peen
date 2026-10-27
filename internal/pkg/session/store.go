// Package session owns Peen's durable transport-neutral session transcript.
package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	blobs                 *db.ContentBlobStore
	clock                 func() time.Time
	newID                 func() uuid.UUID
	maxStoredMessageBytes int
	// maxExecutionStateBytes bounds one execution context or activity
	// payload. Each holds the whole turn context, so it is sized for the
	// context window rather than for one message.
	maxExecutionStateBytes int

	activeMu sync.Mutex
	active   map[uuid.UUID]activeTurn

	feedSignalMu sync.RWMutex
	feedSignal   FeedSignalFunc

	idleCallbackMu sync.RWMutex
	idleCallback   IdleCallbackFunc
}

// OpenWorkspace returns the one durable session belonging to workspace.
func (s *Store) OpenWorkspace(
	ctx context.Context,
	workspace string,
	options OpenSessionOptions,
) (*OpenSessionResult, error) {
	canonicalWorkspace, err := canonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}

	return s.OpenWorkspaceIdentity(ctx, canonicalWorkspace, options)
}

// OpenWorkspaceIdentity binds a caller-owned virtual identity without reading
// the host filesystem. Host callers must use OpenWorkspace for canonical paths.
func (s *Store) OpenWorkspaceIdentity(
	ctx context.Context,
	canonicalWorkspace string,
	options OpenSessionOptions,
) (*OpenSessionResult, error) {
	if strings.TrimSpace(canonicalWorkspace) == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace identity",
		)
	}

	session, err := s.findWorkspaceSession(ctx, canonicalWorkspace)
	if err == nil {
		return &OpenSessionResult{Session: session}, nil
	}

	if !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "find workspace session")
	}

	now := s.now()

	created := &models.Session{
		ID:         s.newID(),
		CreatedAt:  now,
		UpdatedAt:  now,
		RootAgent:  options.RootAgent,
		ModelID:    options.ModelID,
		Workspace:  canonicalWorkspace,
		AgentSpace: options.AgentSpace,
	}
	if err := s.query.Session.WithContext(ctx).Create(created); err != nil {
		existing, lookupErr := s.findWorkspaceSession(ctx, canonicalWorkspace)
		if lookupErr == nil {
			return &OpenSessionResult{Session: existing}, nil
		}

		return nil, ctxerrors.Wrap(err, "create workspace session")
	}

	return &OpenSessionResult{Session: created, Created: true}, nil
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
		blobs:                 handle.ContentBlobs(),
		clock:                 clock,
		newID:                 newID,
		maxStoredMessageBytes: maxStoredMessageBytes,
		maxExecutionStateBytes: max(maxStoredMessageBytes,
			defaultMaxExecutionStateBytes),
		active: make(map[uuid.UUID]activeTurn),
	}, nil
}

// CreateOrResume creates a session or resumes its requested durable ID.
func (s *Store) CreateOrResume(
	ctx context.Context,
	requestedSessionID *uuid.UUID,
	options OpenSessionOptions,
) (*OpenSessionResult, error) {
	if requestedSessionID != nil {
		session, err := s.findSession(ctx, *requestedSessionID)
		if err == nil {
			return &OpenSessionResult{Session: session}, nil
		}

		if !errors.Is(err, commerr.ErrNotFound) {
			return nil, ctxerrors.Wrap(err, "resume session")
		}
	}

	sessionID := s.newID()
	if requestedSessionID != nil {
		sessionID = *requestedSessionID
	}

	now := s.now()

	session := &models.Session{
		ID:        sessionID,
		CreatedAt: now,
		UpdatedAt: now,
		RootAgent: options.RootAgent,
		ModelID:   options.ModelID,
	}
	if err := s.query.Session.WithContext(ctx).Create(session); err != nil {
		if requestedSessionID != nil {
			existing, resumeErr := s.findSession(ctx, *requestedSessionID)
			if resumeErr == nil {
				return &OpenSessionResult{Session: existing}, nil
			}
		}

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

	lease, err := newTurnLease(sessionID, s.newID())
	if err != nil {
		return Lease{}, err
	}

	if err := s.reserveLease(lease); err != nil {
		return Lease{}, err
	}

	if err := s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		return s.admitTurn(ctx, tx, sessionID, lease, input)
	}); err != nil {
		s.ReleaseTurn(lease)

		return Lease{}, ctxerrors.Wrap(err, "start turn transaction")
	}

	return lease, nil
}

// admitTurn writes a new running turn, its initial transcript, and any
// opening notices it admits, all inside the caller's feed transaction.
func (s *Store) admitTurn(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	lease Lease,
	input StartTurnInput,
) error {
	revision, err := s.admissionStopRevision(ctx, tx, sessionID,
		input.ExpectedStopRevision)
	if err != nil {
		return ctxerrors.Wrap(err, "validate turn stop revision")
	}

	turn := &models.Turn{
		ID: lease.TurnID, SessionID: lease.SessionID,
		RequestID:          input.RequestID,
		Workspace:          input.Workspace,
		State:              models.TurnStateRunning,
		StartedAt:          s.now(),
		WorkerGenerationID: input.WorkerGenerationID,
		StopRevision:       revision,
	}
	if err := tx.Turn.WithContext(ctx).Create(turn); err != nil {
		return ctxerrors.Wrap(err, "create running turn")
	}

	if _, err := s.appendTranscript(
		ctx,
		tx,
		lease,
		input.Messages,
		input.Events,
	); err != nil {
		return ctxerrors.Wrap(err, "append initial turn transcript")
	}

	if _, err := s.deliverOpeningNotices(ctx, tx, lease, input); err != nil {
		return ctxerrors.Wrap(err, "admit opening session notices")
	}

	return nil
}

// RegisterCancellation associates cancellation with one exact active lease.
func (s *Store) RegisterCancellation(lease Lease, cancel context.CancelFunc) {
	s.activeMu.Lock()

	active, ok := s.active[lease.SessionID]
	if !ok || active.turnID != lease.TurnID || active.leaseID != lease.ID {
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

// ReleaseTurn removes a lease only when it is still the registered turn.
func (s *Store) ReleaseTurn(lease Lease) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, ok := s.active[lease.SessionID]
	if !ok || active.turnID != lease.TurnID || active.leaseID != lease.ID {
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

func (s *Store) findWorkspaceSession(
	ctx context.Context,
	workspace string,
) (*models.Session, error) {
	session := s.query.Session

	result, err := session.WithContext(ctx).
		Where(session.Workspace.Eq(workspace)).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query workspace session")
	}

	return result, nil
}

func canonicalWorkspace(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace is required",
		)
	}

	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", ctxerrors.Wrap(err, "make workspace absolute")
	}

	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve workspace symlinks")
	}

	info, err := os.Stat(canonical)
	if err != nil {
		return "", ctxerrors.Wrap(err, "stat workspace")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace is not a directory",
		)
	}

	return canonical, nil
}

func (s *Store) reserveLease(lease Lease) error {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	if _, exists := s.active[lease.SessionID]; exists {
		return ctxerrors.Wrap(ErrSessionBusy, "session turn lease")
	}

	s.active[lease.SessionID] = activeTurn{
		turnID: lease.TurnID, leaseID: lease.ID,
	}

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
