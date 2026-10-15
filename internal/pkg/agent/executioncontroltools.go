package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const resumeExecutionDescription = `Continue retained work only when the user
explicitly asks to resume it. First inspect its saved context and history with
searchExecutions/readExecution and inspect the current workspace. Supply the
executionId and controlRevision from the inspected execution metadata.
Resumes only paused members of that execution's subtree. Completed, failed
and cancelled members keep their outcomes. For a stopped deferred inbox message,
select its completed source execution and also supply deferredRequestId and
deferredStopRevision from readExecution inbox. This admits only that message
without restarting its completed source. A root waits until the current
turn releases the session; a child may run alongside the current turn.
A newer Stop or changed target refuses stale requests. Unknown tool effects
still require inspection and resolution before those steps can continue.`

const resumeExecutionSchema = `{
  "type": "object",
  "properties": {
    "executionId": {"type": "string", "format": "uuid"},
    "controlRevision": {"type": "integer", "minimum": 0},
    "deferredRequestId": {"type": "string", "format": "uuid"},
    "deferredStopRevision": {"type": "integer", "minimum": 0}
  },
  "required": ["executionId", "controlRevision"],
  "additionalProperties": false
}`

type resumeExecutionInput struct {
	ExecutionID          uuid.UUID  `json:"executionId"`
	ControlRevision      int64      `json:"controlRevision"`
	DeferredRequestID    *uuid.UUID `json:"deferredRequestId,omitempty"`
	DeferredStopRevision *int64     `json:"deferredStopRevision,omitempty"`
}

type resumeExecutionOutput struct {
	ExecutionID       uuid.UUID   `json:"executionId"`
	ResumedIDs        []uuid.UUID `json:"resumedIds"`
	DeferredRequestID *uuid.UUID  `json:"deferredRequestId,omitempty"`
}

func (d *launchAgentDeps) resumeExecution(
	ctx context.Context,
	input resumeExecutionInput,
) (resumeExecutionOutput, error) {
	result := resumeExecutionOutput{
		ExecutionID:       input.ExecutionID,
		DeferredRequestID: input.DeferredRequestID,
	}
	native := d.runtime.nativeWorkflowRunner()

	store, ok := d.runtime.store.(session.ExecutionAgentControls)
	if native == nil || !ok {
		return result, ctxerrors.Wrap(commerr.ErrNotImplemented,
			"execution resume requires a session workflow worker")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return result, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"execution resume requires an active workflow tool")
	}

	var err error

	result.ResumedIDs, err = store.ResumeExecutionFromActivity(ctx,
		d.sessionID, input.ExecutionID, session.ResumeExecutionInput{
			SourceLease: lease, ControlRevision: input.ControlRevision,
			DeferredRequestID:    input.DeferredRequestID,
			DeferredStopRevision: input.DeferredStopRevision,
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "resume inspected execution")
	}

	if err := native.nudgeAdmittedExecutions(ctx); err != nil {
		return result, ctxerrors.Wrap(err, "signal resumed executions")
	}

	return result, nil
}
