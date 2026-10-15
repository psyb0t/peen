package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type ResumeExecutionInput struct {
	SourceLease          ActivityLease `json:"sourceLease"`
	ControlRevision      int64         `json:"controlRevision"`
	DeferredRequestID    *uuid.UUID    `json:"deferredRequestId,omitempty"`
	DeferredStopRevision *int64        `json:"deferredStopRevision,omitempty"`
}

type ExecutionAgentControls interface {
	ResumeExecutionFromActivity(context.Context, uuid.UUID, uuid.UUID,
		ResumeExecutionInput) ([]uuid.UUID, error)
	ResolveExecutionActivityFromActivity(context.Context, uuid.UUID, uuid.UUID,
		ResolveExecutionControlInput) (*models.ExecutionActivity, error)
}

var _ ExecutionAgentControls = (*Store)(nil)

// ResumeExecutionFromActivity fences both the caller and the inspected target.
// A Stop that commits first prevents the caller from resuming retained work.
func (s *Store) ResumeExecutionFromActivity(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input ResumeExecutionInput,
) ([]uuid.UUID, error) {
	if err := validateDeferredResumeSelection(input); err != nil {
		return nil, err
	}

	var changed []uuid.UUID

	err := s.query.Transaction(func(tx *repositories.Query) error {
		source, err := s.authorizeExecutionControl(ctx, tx, sessionID,
			executionID, input)
		if err != nil {
			return ctxerrors.Wrap(err, "authorize agent execution resume")
		}

		if input.DeferredRequestID != nil {
			return s.resumeDeferredInput(ctx, tx, sessionID,
				executionID, source, input)
		}

		payload, err := json.Marshal(struct {
			SourceExecutionID uuid.UUID `json:"sourceExecutionId"`
			ResumeExecutionInput
		}{source.ID, input})
		if err != nil {
			return ctxerrors.Wrap(err, "encode agent resume provenance")
		}

		changed, err = s.changeExecutionSubtree(ctx, tx, sessionID,
			executionID, ExecutionEventTypeResumed, "", payload)

		return err
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "agent execution resume transaction")
	}

	ctxscope.GetLogger(ctx).Info("agent execution resume recorded",
		"session_id", sessionID, "execution_id", executionID,
		"source_activity_id", input.SourceLease.ID,
		"changed_count", len(changed))

	return changed, nil
}

func (s *Store) authorizeExecutionControl(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, executionID uuid.UUID,
	input ResumeExecutionInput,
) (*models.Execution, error) {
	if executionID == uuid.Nil || input.ControlRevision < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution control target")
	}

	activity, source, err := s.activityForLease(ctx, tx, sessionID,
		input.SourceLease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution control caller")
	}

	if activity.State != models.ActivityStateRunning ||
		activity.Kind != models.ActivityKindTool ||
		source.State != models.ExecutionStateRunning {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"execution control caller is no longer active")
	}

	target, err := s.findExecution(ctx, tx, sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution control target")
	}

	if target.ControlRevision != input.ControlRevision {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"execution control target changed after inspection")
	}

	return source, nil
}
