package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const resolveExecutionActivityDescription = `Record an inspection decision for
an uncertain step in retained work. Inspect the current workspace, activity
input, attempt and saved context with readExecution first. Supply activityId,
the execution's inspected controlRevision, the activity attempt, and evidence
describing what you actually checked. Choose retry only when repeating the
effect is safe. Choose accept only when you know the outcome and can supply
the exact complete output checkpoint and context objects required by that
saved activity. Do not invent an outcome or discard saved context.
This records the decision and the caller in history. A paused execution stays
paused until resumeExecution. An admitted execution can continue once all
uncertain steps are resolved. A newer Stop or changed target refuses stale
requests. Leave the step uncertain when the evidence is insufficient.`

const resolveExecutionActivitySchema = `{
  "type": "object",
  "properties": {
    "activityId": {"type": "string", "format": "uuid"},
    "controlRevision": {"type": "integer", "minimum": 0},
    "attempt": {"type": "integer", "minimum": 1},
    "resolution": {"type": "string", "enum": ["retry", "accept"]},
    "evidence": {"type": "string", "minLength": 1},
    "output": {"type": "object"},
    "context": {"type": "object"}
  },
  "required": ["activityId", "controlRevision", "attempt", "resolution",
    "evidence"],
  "additionalProperties": false
}`

type resolveExecutionActivityInput struct {
	ActivityID      uuid.UUID `json:"activityId"`
	ControlRevision int64     `json:"controlRevision"`
	session.ResolveActivityInput
}

type resolveExecutionActivityOutput struct {
	ExecutionID uuid.UUID            `json:"executionId"`
	ActivityID  uuid.UUID            `json:"activityId"`
	State       models.ActivityState `json:"state"`
	Attempt     int64                `json:"attempt"`
}

func (d *launchAgentDeps) resolveExecutionActivity(
	ctx context.Context,
	input resolveExecutionActivityInput,
) (resolveExecutionActivityOutput, error) {
	var result resolveExecutionActivityOutput

	native := d.runtime.nativeWorkflowRunner()

	store, ok := d.runtime.store.(session.ExecutionAgentControls)
	if native == nil || !ok {
		return result, ctxerrors.Wrap(commerr.ErrNotImplemented,
			"activity inspection requires a session workflow worker")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return result, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"activity inspection requires an active workflow tool")
	}

	activity, err := store.ResolveExecutionActivityFromActivity(ctx,
		d.sessionID, input.ActivityID, session.ResolveExecutionControlInput{
			SourceLease: lease, ControlRevision: input.ControlRevision,
			ResolveActivityInput: input.ResolveActivityInput,
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "record inspected activity outcome")
	}

	result = resolveExecutionActivityOutput{
		ExecutionID: activity.ExecutionID, ActivityID: activity.ID,
		State: activity.State, Attempt: activity.Attempt,
	}
	if err := native.nudgeAdmittedExecutions(ctx); err != nil {
		return result, ctxerrors.Wrap(err, "signal inspected execution")
	}

	return result, nil
}
