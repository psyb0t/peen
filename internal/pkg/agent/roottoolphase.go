package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/psyb0t/ctxerrors"
)

func (c *rootWorkflowCallbacks) toolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
) (workflowToolPhaseState, error) {
	lease, err := c.runtime.validateWorkflowPhaseActivity(ctx, input,
		c.generationID)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "validate root tool phase")
	}

	shared, release, err := c.acquire(ctx, input.Tool.Round.Execution, false)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "acquire root tool phase")
	}
	defer release()

	prepared, scope, err := c.restoreToolPhaseScope(ctx, shared, input)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "restore root tool phase scope")
	}

	input.State.RootProjection = nil

	if input.Phase == workflowToolPhasePre {
		input.State.StreamAttempt = lease.Attempt
		if err := prepared.startWorkflowToolPhase(ctx, input); err != nil {
			return input.State, ctxerrors.Wrap(err, "start root tool phase")
		}

		input.State.StartedAt = prepared.turn.toolStart[input.Tool.Call.ID]
	}

	state, err := runDirectoryWorkflowToolPhase(ctx, scope, input,
		workflowToolOptions{
			Timeout:         c.runtime.toolTimeout,
			MaxResultTokens: c.runtime.maxToolResultTokens,
			Counter:         modelTokenCounter(prepared.model),
		})
	if err != nil {
		return state, ctxerrors.Wrap(err, "run root tool phase")
	}

	if err := prepared.checkpointRootWorkflowQuestion(ctx, c.runtime, input,
		&state); err != nil {
		return state, ctxerrors.Wrap(err, "retain root question phase")
	}

	if input.Phase == workflowToolPhaseProject {
		if err := prepared.projectWorkflowToolPhase(ctx, scope, input,
			&state); err != nil {
			return state, ctxerrors.Wrap(err, "project root tool phase")
		}
	}

	state.RootProjection = prepared.rootToolProjection(shared, lease.ID)

	return state, nil
}

func (c *rootWorkflowCallbacks) restoreToolPhaseScope(
	ctx context.Context,
	shared *rootCallbackScope,
	input workflowToolPhaseInput,
) (*preparedTurn, *directoryScope, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Tool.Round.Execution.Context,
		&conversation); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "decode root phase context")
	}

	seed := *shared.prepared
	seed.workflowInbox = false

	seed.turn = newRootPhaseTurn(shared.prepared.turn)
	if !input.State.StartedAt.IsZero() {
		seed.turn.toolStart = map[string]time.Time{
			input.Tool.Call.ID: input.State.StartedAt,
		}
	}

	seed.attachPublisher(ctx)

	prepared, scope, err := c.runtime.restoreRootWorkflowScope(ctx, &seed,
		conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore private root scope")
	}

	prepared.workflowInbox = shared.prepared.workflowInbox

	return prepared, scope, nil
}

func (p *preparedTurn) startWorkflowToolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
) error {
	adapter, err := p.workflowPhaseToolAdapter(ctx, input)
	if err != nil {
		return ctxerrors.Wrap(err, "restore root pre-hook stream")
	}

	event := workflowPhaseCallEvent(input.Tool)
	if err := p.onToolCallStart(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "record root phase tool start")
	}

	if err := adapter.OnToolCallStart(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "stream root phase tool start")
	}

	return nil
}

func (p *preparedTurn) projectWorkflowToolPhase(
	ctx context.Context,
	scope *directoryScope,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) error {
	adapter, err := p.workflowPhaseToolAdapter(ctx, input)
	if err != nil {
		return ctxerrors.Wrap(err, "restore root projection stream")
	}

	if err := p.checkpointWorkflowTool(ctx, scope, adapter,
		workflowPhaseCallEvent(input.Tool), state.Outcome); err != nil {
		return ctxerrors.Wrap(err, "record root phase projection")
	}

	state.Outcome.Context, err = rootWorkflowToolContext(input.Tool, scope)
	if err != nil {
		return ctxerrors.Wrap(err, "retain projected root scope")
	}

	return nil
}
