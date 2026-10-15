package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *Controller) callAgentExecutionControl(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	if r.Operation == executionResumeFromActivity {
		return c.resumeExecutionFromActivity(ctx, r)
	}

	if r.ResolveControl.SourceLease.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"inspection caller generation differs from its connection")
	}

	result, err := c.store.ResolveExecutionActivityFromActivity(ctx,
		c.sessionID, r.ActivityID, r.ResolveControl)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve inspected worker activity")
	}

	return result, nil
}

func (s *Store) ResolveExecutionActivityFromActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
	input session.ResolveExecutionControlInput,
) (*models.ExecutionActivity, error) {
	return executionCall[*models.ExecutionActivity](ctx, s, sessionID,
		executionRequest{
			Operation: executionResolveFromActivity, ActivityID: activityID,
			ResolveControl: input,
		})
}
