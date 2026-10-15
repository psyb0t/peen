package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) refreshChildPhaseEvents(
	ctx context.Context,
	actor *childWorkflowActor,
) error {
	// Inbox emitters share this lock, so they cannot overtake the snapshot.
	actor.sink.mu.Lock()
	defer actor.sink.mu.Unlock()

	stored, err := c.runtime.store.GetAgentRun(ctx, c.runtime.sessionID,
		actor.run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child event window size")
	}

	buffer := actor.run.events
	retained := newAgentRunEventBuffer(buffer.maxCount, buffer.maxBytes)

	cursor := max(int64(0), stored.EventCount-int64(buffer.maxCount))
	for cursor < stored.EventCount {
		page, err := c.runtime.store.ListAgentRunEvents(ctx,
			c.runtime.sessionID, actor.run.ID,
			session.ListAgentRunEventsOptions{
				Cursor: cursor, Limit: session.MaximumPageLimit,
			})
		if err != nil {
			return ctxerrors.Wrap(err, "read child event window page")
		}

		previous := cursor

		for _, event := range page.Items {
			if event.Sequence > stored.EventCount {
				break
			}

			if event.Sequence != cursor+1 {
				return ctxerrors.Wrap(commerr.ErrConflict,
					"child event window is not contiguous")
			}

			retained.AppendStored(retainedChildRegistryEvent(event))
			cursor = event.Sequence
		}

		if cursor == previous {
			return ctxerrors.Wrap(commerr.ErrNotFound,
				"child event window is incomplete")
		}
	}

	buffer.replaceStoredWindow(retained)

	return nil
}

func (b *agentRunEventBuffer) replaceStoredWindow(
	retained *agentRunEventBuffer,
) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if retained.total < b.total {
		return
	}

	b.events, b.byteLen, b.total = retained.events, retained.byteLen,
		retained.total
}
