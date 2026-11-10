package agent

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// emptyToolArguments stands in for the arguments of a tool call that
	// carried none, because a tool event needs valid JSON arguments.
	emptyToolArguments = "{}"

	acceptedToolResultRefusal = `accept on a tool call step needs output to be the tool's result, {"Content": "<result text>", "IsError": false}` //nolint:lll // One model-facing refusal.
)

const resolveExecutionActivityDescription = `Record an inspection decision for
an uncertain step in retained work. Inspect the current workspace, activity
input, attempt and saved context with readExecution first. Supply activityId,
the execution's inspected controlRevision, the activity attempt, and evidence
describing what you actually checked. Choose retry only when repeating the
effect is safe. Choose accept only when you know the outcome. For a tool call
step, output is the result the tool would have returned, as
{"Content": "<result text>", "IsError": false}, and Peen keeps the rest of
the step's state. For another step, output is that step's complete output
checkpoint object. Leave context out to keep the step's saved context. Do not
invent an outcome.
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

	if err := d.prepareResolution(ctx, &input); err != nil {
		return result, err
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

// prepareResolution shapes the agent's decision into what the store records.
// An accept gets the rest of its checkpoint from the step. A retry runs the
// step again, so an outcome the agent wrote anyway has nothing to record and
// is dropped rather than refused.
func (d *launchAgentDeps) prepareResolution(
	ctx context.Context,
	input *resolveExecutionActivityInput,
) error {
	switch input.Resolution {
	case session.ActivityResolutionAccept:
		return d.completeAcceptedCheckpoint(ctx, input)
	case session.ActivityResolutionRetry:
		input.Output, input.Context = nil, nil
	}

	return nil
}

// completeAcceptedCheckpoint fills in what an accepted step saves from the
// step itself. A missing context takes the step's saved context. A tool call
// step's output is Peen's own phase state, which the next phase of that call
// reads back, so it is built from the state the step started with and takes
// only the tool result from the agent's output.
func (d *launchAgentDeps) completeAcceptedCheckpoint(
	ctx context.Context,
	input *resolveExecutionActivityInput,
) error {
	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrNotImplemented,
			"activity inspection requires execution storage")
	}

	activity, err := store.GetExecutionActivity(ctx, d.sessionID,
		input.ActivityID)
	if err != nil {
		return ctxerrors.Wrap(err, "read accepted activity")
	}

	if len(input.Context) == 0 && activity.ContextJSON != "" {
		input.Context = json.RawMessage(activity.ContextJSON)
	}

	if activity.Kind != models.ActivityKindTool ||
		activity.Name != toolPhaseActivityName {
		return nil
	}

	output, err := acceptedToolPhaseOutput(activity.InputJSON, input.Output)
	if err != nil {
		return ctxerrors.Wrap(err, "build accepted tool call checkpoint")
	}

	input.Output = output

	return nil
}

func acceptedToolPhaseOutput(
	inputJSON string,
	output json.RawMessage,
) (json.RawMessage, error) {
	var phase workflowToolPhaseInput
	if err := json.Unmarshal([]byte(inputJSON), &phase); err != nil {
		return nil, ctxerrors.Wrap(err, "decode tool call step input")
	}

	result, err := acceptedToolResult(output)
	if err != nil {
		return nil, err
	}

	// The projections buffer what one phase run sends to the feed. An
	// accepted step sent nothing, so it keeps none.
	state := phase.State
	state.LastPhase = phase.Phase
	state.Outcome.Result = result
	state.RootProjection, state.ChildProjection = nil, nil

	if state.Event == nil {
		arguments := phase.Tool.Call.Arguments
		if !json.Valid(arguments) {
			arguments = json.RawMessage(emptyToolArguments)
		}

		state.Event = &workflowToolEventState{
			RawArguments: bytes.Clone(arguments),
		}
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode accepted tool call checkpoint")
	}

	return encoded, nil
}

// acceptedToolResult reads the tool result from an accepted output, given
// either as a whole phase state or as the tool result itself.
func acceptedToolResult(output json.RawMessage) (elelem.ToolResult, error) {
	var state workflowToolPhaseState
	if err := json.Unmarshal(output, &state); err == nil &&
		hasToolResult(state.Outcome.Result) {
		return state.Outcome.Result, nil
	}

	var result elelem.ToolResult
	if err := json.Unmarshal(output, &result); err == nil &&
		hasToolResult(result) {
		return result, nil
	}

	return elelem.ToolResult{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
		acceptedToolResultRefusal)
}

func hasToolResult(result elelem.ToolResult) bool {
	return result.Content != "" || result.IsError || len(result.Images) > 0
}
