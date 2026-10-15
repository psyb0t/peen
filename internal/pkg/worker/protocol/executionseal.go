package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) ReserveExecutionCompletion(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
) (*session.ExecutionCompletionSeal, error) {
	return executionCall[*session.ExecutionCompletionSeal](ctx, s, sessionID,
		executionRequest{
			Operation: executionReserveCompletion, Lease: lease,
		})
}
