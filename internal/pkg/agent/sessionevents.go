package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	sessionEventsOpenTag     = events.PromptOpenTag
	sessionEventsCloseTag    = events.PromptCloseTag
	eventElementOpen         = events.PromptElementOpen
	eventElementClose        = events.PromptElementClose
	sessionEventsPreamble    = events.PromptPreamble
	sessionEventsDroppedLead = events.PromptDroppedLead
)

// injectSessionEvents drains the session's pending events at a tool boundary
// and returns them as one coalesced message. Coalescing matters: a burst of
// finishing jobs must not become one message per job.
//
// It returns nil when nothing is pending, which injects nothing.
func (p *preparedTurn) injectSessionEvents(
	ctx context.Context,
	_ *elelem.ToolEvent,
) (*elelem.MessageInjection, error) {
	// A workflow tool phase runs on a buffering turn with no store of its
	// own and drains through the runtime instead, so only a turn with
	// neither path has no durable session to read.
	if p.opened == nil || p.opened.Session == nil || p.turn == nil ||
		(p.drainNotices == nil && p.turn.store == nil) {
		return nil, nil //nolint:nilnil // No durable session has notices.
	}

	batch, err := p.drainSessionEvents(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "drain durable session notices")
	}

	if len(batch.Notices) == 0 {
		return nil, nil //nolint:nilnil // Nothing pending injects nothing.
	}

	if err := p.turn.emit(ctx, EventTypeSessionEvents, sessionEventsPayload{
		Notices: batch.Notices,
		Dropped: batch.Dropped,
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "emit delivered session events")
	}

	runtimecontext.Logger(ctx).Info(
		"session events delivered",
		"event_count", len(batch.Notices),
		"dropped_count", batch.Dropped,
	)

	content := renderModelEvents(batch)
	if content == "" {
		return nil, nil //nolint:nilnil // Only turn-end notices: no injection.
	}

	return &elelem.MessageInjection{
		Type:    elelem.RoleUser,
		Content: content,
	}, nil
}

func (p *preparedTurn) drainSessionEvents(
	ctx context.Context,
) (events.Batch, error) {
	if p.drainNotices != nil {
		batch, err := p.drainNotices(ctx, p.opened.Session.ID)
		if err != nil {
			return events.Batch{}, ctxerrors.Wrap(err,
				"consume runtime session notices")
		}

		return batch, nil
	}

	notices, err := p.turn.store.DrainSessionNotices(ctx, p.opened.Session.ID)
	if err != nil {
		return events.Batch{}, ctxerrors.Wrap(err,
			"consume standalone session notices")
	}

	if p.eventBus != nil {
		ids := make([]uuid.UUID, len(notices))
		for index, notice := range notices {
			ids[index] = notice.ID
		}

		p.eventBus.Discard(p.opened.Session.ID, ids)
	}

	return sessionNoticeBatch(notices), nil
}

func sessionNoticeBatch(notices []*models.SessionNotice) events.Batch {
	batch := events.Batch{Notices: make([]events.Notice, 0, len(notices))}
	for _, notice := range notices {
		batch.Notices = append(batch.Notices, events.Notice{
			ID:        notice.ID,
			SessionID: notice.SessionID,
			Type:      notice.Type,
			Source:    notice.Source,
			Summary:   notice.Summary,
			Data:      []byte(notice.DataJSON),
			Delivery:  string(notice.Delivery),
			CreatedAt: notice.CreatedAt,
		})
	}

	return batch
}

// renderSessionEvents builds the quoted data block the model receives. The
// session store renders the same block into a child's inbox.
func renderSessionEvents(batch events.Batch) string {
	return events.RenderPrompt(batch)
}

// sessionEventPrompt is one untrusted notice rendered as a JSON value.
type sessionEventPrompt = events.PromptEvent
