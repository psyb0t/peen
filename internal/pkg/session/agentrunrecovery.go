package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type agentRunOwnerKey struct{}

// PrepareAgentRunRecovery settles stale children of this session alone.
// Embedded runtimes use controller startup recovery and pass no generation.
func (s *Store) PrepareAgentRunRecovery(
	ctx context.Context,
	sessionID, generationID uuid.UUID,
) (int, error) {
	if generationID == uuid.Nil {
		return 0, nil
	}

	count := 0

	err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findSessionWithQuery(ctx, tx, sessionID); err != nil {
			return ctxerrors.Wrap(err, "find child recovery session")
		}

		repository := tx.AgentRun

		runs, err := repository.WithContext(ctx).Where(
			repository.SessionID.Eq(sessionID),
			repository.State.Eq(string(models.AgentRunStateRunning)),
			repository.WorkerGenerationID.Neq(generationID.String()),
		).Find()
		if err != nil {
			return ctxerrors.Wrap(err, "find stale worker children")
		}

		for _, run := range runs {
			if err := s.interruptAgentRun(ctx, tx, run.ID); err != nil {
				return ctxerrors.Wrap(err, "settle stale worker child")
			}

			if err := s.interruptChildModelRuns(
				ctx, tx, sessionID, run.ID,
			); err != nil {
				return ctxerrors.Wrap(err, "settle child model audit")
			}

			count++
		}

		return nil
	})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "prepare worker child recovery")
	}

	return count, nil
}

// WithAgentRunOwner fences child writes against a replaced worker. Apply it
// only to worker mutations; historical reads remain available to new workers.
func WithAgentRunOwner(
	ctx context.Context,
	generationID uuid.UUID,
) context.Context {
	return context.WithValue(ctx, agentRunOwnerKey{}, generationID)
}

func requireAgentRunOwner(ctx context.Context, run *models.AgentRun) error {
	generationID, scoped := ctx.Value(agentRunOwnerKey{}).(uuid.UUID)
	if scoped && run.WorkerGenerationID != generationID.String() {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"child belongs to another worker",
		)
	}

	return nil
}

// ClaimAgentRun resumes an interrupted child or transfers a stale worker's
// running child. A current owner, cancelled child or terminal child cannot
// be claimed. The conditional update prevents two owners claiming one run.
func (s *Store) ClaimAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input ClaimAgentRunInput,
) (*models.AgentRun, error) {
	var result *models.AgentRun

	err := s.query.Transaction(func(tx *repositories.Query) error {
		run, err := s.findAgentRunWithQuery(ctx, tx, sessionID, agentRunID)
		if err != nil {
			return ctxerrors.Wrap(err, "find child for recovery")
		}

		if run.CancelRequested || run.State != input.PreviousState ||
			run.WorkerGenerationID != input.PreviousGenerationID ||
			!recoverableAgentRun(run, input.GenerationID) {
			return ctxerrors.Wrap(
				commerr.ErrConflict,
				"child is not recoverable",
			)
		}

		if err := claimAgentRunWithQuery(ctx, tx, run,
			input.GenerationID); err != nil {
			return ctxerrors.Wrap(err, "claim recovered child ownership")
		}

		result = run

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "recover durable child")
	}

	return result, nil
}

func claimAgentRunWithQuery(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
	generationID uuid.UUID,
) error {
	repository := tx.AgentRun

	updated, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(run.ID),
		repository.SessionID.Eq(run.SessionID),
		repository.State.Eq(string(run.State)),
		repository.WorkerGenerationID.Eq(run.WorkerGenerationID),
		repository.CancelRequested.Is(false),
	).UpdateSimple(
		repository.State.Value(string(models.AgentRunStateRunning)),
		repository.WorkerGenerationID.Value(generationID.String()),
		repository.FailureClassification.Value(""),
		repository.FailureDetail.Value(""),
		repository.EndedAt.Null(),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "claim interrupted child")
	}

	if updated.RowsAffected != 1 {
		return ctxerrors.Wrap(commerr.ErrConflict, "child claim lost")
	}

	run.State = models.AgentRunStateRunning
	run.WorkerGenerationID = generationID.String()
	run.FailureClassification, run.FailureDetail = "", ""
	run.EndedAt = nil

	return nil
}

func recoverableAgentRun(run *models.AgentRun, generationID uuid.UUID) bool {
	if run.State == models.AgentRunStateInterrupted {
		return true
	}

	return run.State == models.AgentRunStateRunning &&
		generationID != uuid.Nil &&
		run.WorkerGenerationID != generationID.String()
}
