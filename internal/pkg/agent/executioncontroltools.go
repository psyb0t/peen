package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

//nolint:lll // Model-facing paragraphs stay on one line.
const resumeExecutionDescription = `Continue paused work, only when the user asks for it. First inspect the work's saved context and history with searchExecutions and readExecution, and check the current workspace. Pass the executionId and the controlRevision that readExecution reported; a newer stop or a changed target since then refuses the call. Only the paused parts of that work resume, and finished, failed, and cancelled parts keep their outcomes.

To deliver a message that was held back when its work stopped, pass that work's executionId with the deferredRequestId and deferredStopRevision readExecution listed in its inbox; this delivers only that message. Resumed root work starts after the current turn ends, while a resumed child can run alongside it. A tool call whose effect is unknown is not repeated: the resumed work reads it as an error saying so, unless you record what it did with resolveExecutionActivity first.`

// resumeExecutionNotFoundFormat is the refusal for an executionId the session
// does not have, which a model that lost track of the real ID can send. It
// replaces the store's internal error with what the agent can do about it.
const resumeExecutionNotFoundFormat = "this session has no execution %s. Find retained work and its executionId with searchExecutions, or do the work directly" //nolint:lll // One model-facing sentence.

// resumedRootNote tells the agent that resumed root work cannot run while its
// own turn holds the session, so waiting for it in this turn never ends.
const resumedRootNote = "This work starts only after the current turn ends. Do not wait for it with waitMessages or another tool, it cannot run until this turn is over. Finish what this turn still needs, then end the turn." //nolint:lll // One model-facing note.

//nolint:lll // Parameter descriptions stay on one line.
const resumeExecutionSchema = `{
  "type": "object",
  "properties": {
    "executionId": {"type": "string", "format": "uuid", "description": "The work to resume, from searchExecutions or readExecution."},
    "controlRevision": {"type": "integer", "minimum": 0, "description": "The controlRevision readExecution reported for that work."},
    "deferredRequestId": {"type": "string", "format": "uuid", "description": "A held-back message's requestId from readExecution's inbox. Only to deliver that message."},
    "deferredStopRevision": {"type": "integer", "minimum": 0, "description": "The stopRevision readExecution's inbox listed with that message."}
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
	Note              string      `json:"note,omitempty"`
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
	if errors.Is(err, commerr.ErrNotFound) {
		return result, ctxerrors.Wrapf(commerr.ErrNotFound,
			resumeExecutionNotFoundFormat, input.ExecutionID)
	}

	if err != nil {
		return result, ctxerrors.Wrap(err, "resume inspected execution")
	}

	// A step left with an unknown effect would otherwise hold the resumed
	// work in reconciliation until someone resolves it, and the agent has
	// already been told to check the workspace before it resumed.
	if err := native.settleInterruptedTools(ctx, d.sessionID,
		result.ResumedIDs, resumedToolEvidence,
		settledByAgentResumedExecution); err != nil {
		return result, ctxerrors.Wrap(err, "settle resumed tool calls")
	}

	if err := native.nudgeAdmittedExecutions(ctx); err != nil {
		return result, ctxerrors.Wrap(err, "signal resumed executions")
	}

	isRoot, err := d.resumedRootExecution(ctx, input.ExecutionID)
	if err != nil {
		return result, err
	}

	if isRoot {
		result.Note = resumedRootNote
	}

	return result, nil
}

// resumedRootExecution reports whether executionID is a root execution, which
// waits for the current turn to end before it runs.
func (d *launchAgentDeps) resumedRootExecution(
	ctx context.Context,
	executionID uuid.UUID,
) (bool, error) {
	store, ok := d.runtime.store.(session.ExecutionRecords)
	if !ok {
		return false, nil
	}

	execution, err := store.GetExecution(ctx, d.sessionID, executionID)
	if err != nil {
		return false, ctxerrors.Wrap(err, "read resumed execution")
	}

	return execution.Kind == models.ExecutionKindRoot, nil
}
