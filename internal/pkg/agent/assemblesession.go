package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

// NewSessionRuntime shares assembly dependencies and the global turn limit.
// The caller owns its shutdown; it does not own the shared storage handle.
func (a *Assembled) NewSessionRuntime(
	ctx context.Context,
	sessionID uuid.UUID,
) (*Runtime, error) {
	if _, err := a.Store.Get(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "read embedded session identity")
	}

	options := a.runtimeOptions
	options.StartupSessionID = sessionID

	runtime, err := NewRuntime(ctx, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "assemble session runtime")
	}

	runtime.turnSlots = a.Runtime.turnSlots

	return runtime, nil
}
