package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type ResolveExecutionControlInput struct {
	SourceLease     ActivityLease `json:"sourceLease"`
	ControlRevision int64         `json:"controlRevision"`
	ResolveActivityInput
}

type executionControlOrigin struct {
	SourceExecutionID uuid.UUID     `json:"sourceExecutionId"`
	SourceLease       ActivityLease `json:"sourceLease"`
	ControlRevision   int64         `json:"controlRevision"`
}

// ResolveExecutionActivityFromActivity records an inspected outcome while
// fencing the caller and target. It leaves a paused execution paused.
func (s *Store) ResolveExecutionActivityFromActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
	input ResolveExecutionControlInput,
) (*models.ExecutionActivity, error) {
	err := s.validateActivityResolution(input.ResolveActivityInput)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(input.ResolveActivityInput)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode agent inspection decision")
	}

	var result *models.ExecutionActivity

	err = s.query.Transaction(func(tx *repositories.Query) error {
		origin, err := s.authorizeActivityResolution(ctx, tx,
			sessionID, activityID, input)
		if err != nil {
			return ctxerrors.Wrap(err, "authorize agent inspection decision")
		}

		result, err = s.resolveActivityWithQuery(ctx, tx, sessionID,
			activityID, input.ResolveActivityInput, string(payload), origin)
		if err != nil {
			return ctxerrors.Wrap(err, "save agent inspection decision")
		}

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "agent inspection transaction")
	}

	ctxscope.GetLogger(ctx).Info("agent activity resolution recorded",
		"session_id", sessionID, "activity_id", activityID,
		"source_activity_id", input.SourceLease.ID,
		"resolution", input.Resolution, "attempt", input.Attempt)

	return result, nil
}

func (s *Store) authorizeActivityResolution(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, activityID uuid.UUID,
	input ResolveExecutionControlInput,
) (*executionControlOrigin, error) {
	activity, err := s.findExecutionActivity(ctx, tx, sessionID, activityID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read inspected activity")
	}

	source, err := s.authorizeExecutionControl(ctx, tx, sessionID,
		activity.ExecutionID, ResumeExecutionInput{
			SourceLease:     input.SourceLease,
			ControlRevision: input.ControlRevision,
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "authorize activity inspection caller")
	}

	return &executionControlOrigin{
		SourceExecutionID: source.ID, SourceLease: input.SourceLease,
		ControlRevision: input.ControlRevision,
	}, nil
}
