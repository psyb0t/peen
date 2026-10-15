package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) afterToolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
	state workflowToolPhaseState,
) error {
	projection := state.ChildProjection
	if projection == nil {
		return nil
	}

	shared, admitted, release, err := c.acquire(ctx, input.Tool.Round, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit committed child phase")
	}
	defer release()

	if err := validateCommittedToolPhase(admitted, c.runtime, input, state,
		projection.ActivityID); err != nil {
		return ctxerrors.Wrap(err, "validate committed child phase")
	}

	if err := c.refreshToolPhaseDirectory(admitted, shared.actor, input,
		&state); err != nil {
		return ctxerrors.Wrap(err, "publish committed child directory")
	}

	events, err := c.readChildPhaseEvents(admitted, shared.actor.run,
		projection)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed child phase events")
	}

	return c.deliverChildPhaseEvents(admitted, shared, projection.ActivityID,
		events)
}

func (c *childWorkflowCallbacks) readChildPhaseEvents(
	ctx context.Context,
	run *AgentRun,
	projection *childPhaseBuffer,
) ([]*models.AgentRunEvent, error) {
	wanted := make(map[uuid.UUID]session.AgentRunEventInput)
	for _, input := range projection.Transcript.Events {
		wanted[input.ID] = input
	}

	matched := make(map[uuid.UUID]*models.AgentRunEvent)

	cursor := projection.EventCursor
	for len(matched) < len(wanted) {
		page, err := c.runtime.store.ListAgentRunEvents(ctx,
			c.runtime.sessionID, run.ID, session.ListAgentRunEventsOptions{
				Cursor: cursor, Limit: session.MaximumPageLimit,
			})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read child phase event page")
		}

		if err := matchChildPhaseEvents(page.Items, wanted,
			matched); err != nil {
			return nil, ctxerrors.Wrap(err, "match committed child event page")
		}

		if len(matched) == len(wanted) {
			break
		}

		if !page.HasMore || page.NextCursor <= cursor {
			return nil, ctxerrors.Wrap(commerr.ErrNotFound,
				"committed child phase event missing")
		}

		cursor = page.NextCursor
	}

	events := make([]*models.AgentRunEvent, 0, len(wanted))
	for _, input := range projection.Transcript.Events {
		events = append(events, matched[input.ID])
	}

	return events, nil
}

func matchChildPhaseEvents(
	events []*models.AgentRunEvent,
	wanted map[uuid.UUID]session.AgentRunEventInput,
	matched map[uuid.UUID]*models.AgentRunEvent,
) error {
	for _, event := range events {
		input, ok := wanted[event.ID]
		if !ok {
			continue
		}

		if event.EventType != input.EventType ||
			event.PayloadJSON != input.PayloadJSON {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"committed child event differs from phase")
		}

		matched[event.ID] = event
	}

	return nil
}

func (c *childWorkflowCallbacks) deliverChildPhaseEvents(
	ctx context.Context,
	shared *childCallbackScope,
	activityID uuid.UUID,
	events []*models.AgentRunEvent,
) error {
	if err := c.refreshChildPhaseEvents(ctx, shared.actor); err != nil {
		return ctxerrors.Wrap(err, "refresh committed child event window")
	}

	if shared.phaseDeliveries == nil {
		shared.phaseDeliveries = make(map[uuid.UUID]int)
	}

	for shared.phaseDeliveries[activityID] < len(events) {
		if err := ctx.Err(); err != nil {
			return ctxerrors.Wrap(err, "deliver retained child phase")
		}

		event := events[shared.phaseDeliveries[activityID]]
		shared.actor.run.AppendStoredEvent(retainedChildRegistryEvent(event))

		if err := c.deliverChildTerminalEvent(shared.actor.run,
			event); err != nil {
			return ctxerrors.Wrap(err, "deliver committed child phase event")
		}

		shared.phaseDeliveries[activityID]++
	}

	return nil
}
