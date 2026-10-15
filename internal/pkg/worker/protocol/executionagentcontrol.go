package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

var _ session.ExecutionAgentControls = (*Store)(nil)

func (c *Controller) resumeExecutionFromActivity(
	ctx context.Context,
	r executionRequest,
) ([]uuid.UUID, error) {
	if r.ResumeControl.SourceLease.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"resume caller generation differs from its connection")
	}

	changed, err := c.store.ResumeExecutionFromActivity(ctx, c.sessionID,
		r.ExecutionID, r.ResumeControl)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resume execution from worker activity")
	}

	return changed, nil
}

func (s *Store) ResumeExecutionFromActivity(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input session.ResumeExecutionInput,
) ([]uuid.UUID, error) {
	return executionCall[[]uuid.UUID](ctx, s, sessionID, executionRequest{
		Operation: executionResumeFromActivity, ExecutionID: executionID,
		ResumeControl: input,
	})
}
