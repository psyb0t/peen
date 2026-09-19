package session

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gen/field"
)

// Worker generation states the store stamps timestamps for. They mirror the
// worker package's own states, which the schema also constrains.
const (
	workerStateStarting = "starting"
	workerStateReady    = "ready"
	workerStateStopped  = "stopped"
	workerStateFailed   = "failed"
)

// CreateWorkerGenerationInput records one worker before it launches, so a
// controller that dies mid-launch still leaves a row an operator can
// reconcile.
type CreateWorkerGenerationInput struct {
	ID              uuid.UUID
	Kind            string
	Profile         string
	ProfileRevision int64
	State           string
	Workspace       string
	SocketPath      string

	// CredentialHash verifies the worker's launch credential. The raw
	// credential is never stored.
	CredentialHash string
}

// CreateWorkerGeneration stores a new generation for one existing session.
func (s *Store) CreateWorkerGeneration(
	ctx context.Context,
	sessionID uuid.UUID,
	input CreateWorkerGenerationInput,
) (*models.WorkerGeneration, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for worker generation")
	}

	if input.ID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker generation requires an ID",
		)
	}

	if strings.TrimSpace(input.CredentialHash) == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker generation requires a credential hash",
		)
	}

	generation := &models.WorkerGeneration{
		ID:              input.ID,
		SessionID:       sessionID,
		Kind:            input.Kind,
		Profile:         input.Profile,
		ProfileRevision: input.ProfileRevision,
		State:           input.State,
		Workspace:       input.Workspace,
		SocketPath:      input.SocketPath,
		CredentialHash:  input.CredentialHash,
		CreatedAt:       s.now(),
	}

	if err := s.query.WorkerGeneration.WithContext(ctx).
		Create(generation); err != nil {
		return nil, ctxerrors.Wrap(err, "create worker generation")
	}

	return generation, nil
}

// UpdateWorkerGenerationState advances one generation's lifecycle.
//
// Reaching ready stamps the start time and reaching a terminal state stamps the
// end time, so a durable record carries how long a worker actually ran.
func (s *Store) UpdateWorkerGenerationState(
	ctx context.Context,
	generationID uuid.UUID,
	state string,
	failureDetail string,
) error {
	generation := s.query.WorkerGeneration

	assignments := []field.AssignExpr{
		generation.State.Value(state),
		generation.FailureDetail.Value(failureDetail),
	}

	now := s.now()

	switch state {
	case workerStateReady:
		assignments = append(assignments, generation.StartedAt.Value(now))
	case workerStateStopped, workerStateFailed:
		assignments = append(assignments, generation.EndedAt.Value(now))
	}

	result, err := generation.WithContext(ctx).
		Where(generation.ID.Eq(generationID)).
		UpdateSimple(assignments...)
	if err != nil {
		return ctxerrors.Wrap(err, "update worker generation state")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "worker generation")
	}

	return nil
}

// RecordWorkerProcess stores the operating system identity of a native worker.
func (s *Store) RecordWorkerProcess(
	ctx context.Context,
	generationID uuid.UUID,
	processID int,
) error {
	generation := s.query.WorkerGeneration

	result, err := generation.WithContext(ctx).
		Where(generation.ID.Eq(generationID)).
		UpdateSimple(generation.ProcessID.Value(processID))
	if err != nil {
		return ctxerrors.Wrap(err, "record the worker process")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "worker generation")
	}

	return nil
}

// RecordWorkerContainer stores the container a Docker worker created. It is
// separate from the state transition because a container ID only exists once
// the daemon has accepted the create call.
func (s *Store) RecordWorkerContainer(
	ctx context.Context,
	generationID uuid.UUID,
	containerID string,
	imageDigest string,
) error {
	generation := s.query.WorkerGeneration

	result, err := generation.WithContext(ctx).
		Where(generation.ID.Eq(generationID)).
		UpdateSimple(
			generation.ContainerID.Value(containerID),
			generation.ImageDigest.Value(imageDigest),
		)
	if err != nil {
		return ctxerrors.Wrap(err, "record the worker container")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "worker generation")
	}

	return nil
}

// RecordWorkerExit stores how a worker ended.
func (s *Store) RecordWorkerExit(
	ctx context.Context,
	generationID uuid.UUID,
	exitCode int,
) error {
	generation := s.query.WorkerGeneration

	result, err := generation.WithContext(ctx).
		Where(generation.ID.Eq(generationID)).
		UpdateSimple(generation.ExitCode.Value(exitCode))
	if err != nil {
		return ctxerrors.Wrap(err, "record the worker exit status")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "worker generation")
	}

	return nil
}

// GetWorkerGeneration reads one generation by ID, scoped to its session so a
// caller cannot read another session's worker record by guessing an ID.
func (s *Store) GetWorkerGeneration(
	ctx context.Context,
	sessionID uuid.UUID,
	generationID uuid.UUID,
) (*models.WorkerGeneration, error) {
	generation := s.query.WorkerGeneration

	stored, err := generation.WithContext(ctx).
		Where(
			generation.ID.Eq(generationID),
			generation.SessionID.Eq(sessionID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get worker generation")
	}

	return stored, nil
}

// ReconfigureExecutionProfile moves one idle session to a different execution
// profile and records why.
//
// It refuses a session with a turn in flight, because changing the environment
// under running work would leave that turn's tool calls attributed to a profile
// that did not run them. The caller stops the session's worker afterwards so
// the next turn starts a new generation under the new profile.
func (s *Store) ReconfigureExecutionProfile(
	ctx context.Context,
	sessionID uuid.UUID,
	toProfile string,
	reason string,
) (*models.SessionProfileDecision, error) {
	if err := validateReconfigureInput(toProfile, reason); err != nil {
		return nil, err
	}

	stored, err := s.findSession(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find session for reconfiguration")
	}

	if s.IsActive(sessionID) {
		return nil, ctxerrors.Wrap(
			ErrSessionBusy,
			"a session with a running turn cannot be reconfigured",
		)
	}

	decision := &models.SessionProfileDecision{
		ID:          s.newID(),
		SessionID:   sessionID,
		FromProfile: stored.ExecutionProfile,
		ToProfile:   toProfile,
		Reason:      reason,
		DecidedAt:   s.now(),
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		session := tx.Session

		result, err := session.WithContext(ctx).
			Where(session.ID.Eq(sessionID)).
			UpdateSimple(session.ExecutionProfile.Value(toProfile))
		if err != nil {
			return ctxerrors.Wrap(err, "update session execution profile")
		}

		if result.RowsAffected == 0 {
			return ctxerrors.Wrap(commerr.ErrNotFound, "session")
		}

		if err := tx.SessionProfileDecision.WithContext(ctx).
			Create(decision); err != nil {
			return ctxerrors.Wrap(err, "record profile decision")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "reconfigure execution profile")
	}

	return decision, nil
}

// validateReconfigureInput refuses a change that would leave the history
// unreadable: a decision with no target, or one with no stated reason.
func validateReconfigureInput(toProfile string, reason string) error {
	if toProfile == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"reconfigure requires a target execution profile",
		)
	}

	if strings.TrimSpace(reason) == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"reconfigure requires a reason",
		)
	}

	return nil
}

// ListSessionProfileDecisions returns one session's profile decision history,
// newest first.
//
//nolint:dupl // The generated SessionProfileDecision query has its own builder.
func (s *Store) ListSessionProfileDecisions(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListProfileDecisionsOptions,
) (*ProfileDecisionPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"find session for profile decision listing",
		)
	}

	decision := s.query.SessionProfileDecision
	query := decision.WithContext(ctx).
		Where(decision.SessionID.Eq(sessionID)).
		Order(decision.DecidedAt.Desc(), decision.ID.Desc())

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.SessionProfileDecision, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list session profile decisions",
		"probe session profile decision page",
	)
	if err != nil {
		return nil, err
	}

	return &ProfileDecisionPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// RecordTurnWorkerGeneration names the worker that ran one turn. It updates
// only the lease's own turn, so a concurrent turn cannot be relabelled.
func (s *Store) RecordTurnWorkerGeneration(
	ctx context.Context,
	lease Lease,
	generationID uuid.UUID,
) error {
	turn := s.query.Turn

	result, err := turn.WithContext(ctx).
		Where(
			turn.ID.Eq(lease.TurnID),
			turn.SessionID.Eq(lease.SessionID),
		).
		UpdateSimple(
			turn.WorkerGenerationID.Value(generationID.String()),
		)
	if err != nil {
		return ctxerrors.Wrap(err, "record the turn worker generation")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "turn for worker record")
	}

	return nil
}

// ListWorkerGenerations returns a stable newest-first page of one session's
// worker generations.
//
//nolint:dupl // The generated WorkerGeneration query has its own builder.
func (s *Store) ListWorkerGenerations(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListWorkerGenerationsOptions,
) (*WorkerGenerationPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"find session for worker generation listing",
		)
	}

	generation := s.query.WorkerGeneration
	query := generation.WithContext(ctx).
		Where(generation.SessionID.Eq(sessionID)).
		Order(generation.CreatedAt.Desc(), generation.ID.Desc())

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.WorkerGeneration, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list worker generations",
		"probe worker generation page",
	)
	if err != nil {
		return nil, err
	}

	return &WorkerGenerationPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// CurrentWorkerGeneration returns the session's newest generation.
func (s *Store) CurrentWorkerGeneration(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.WorkerGeneration, error) {
	generation := s.query.WorkerGeneration

	stored, err := generation.WithContext(ctx).
		Where(generation.SessionID.Eq(sessionID)).
		Order(generation.CreatedAt.Desc(), generation.ID.Desc()).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get current worker generation")
	}

	return stored, nil
}

// ListLiveWorkerGenerations returns every generation a restarted controller
// may still own, newest first.
//
// A controller reconnects to these before replacing anything, so a worker that
// outlived its controller is adopted rather than abandoned.
func (s *Store) ListLiveWorkerGenerations(
	ctx context.Context,
) ([]*models.WorkerGeneration, error) {
	generation := s.query.WorkerGeneration

	stored, err := generation.WithContext(ctx).
		Where(generation.State.In(workerStateStarting, workerStateReady)).
		Order(generation.CreatedAt.Desc(), generation.ID.Desc()).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list live worker generations")
	}

	return stored, nil
}
