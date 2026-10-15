package agent

import (
	"context"
	"math"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/essessey/elelemstream"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type workflowStreamPosition struct {
	next int
	open map[int]struct{}
}

func (p *preparedTurn) restoreWorkflowModelStream(
	ctx context.Context,
) (*workflowStreamProjection, error) {
	position, err := p.readWorkflowStreamPosition(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read retained root stream")
	}

	indexes := make([]int, 0, len(position.open))
	for index := range position.open {
		indexes = append(indexes, index)
	}

	slices.Sort(indexes)

	for _, index := range indexes {
		if err := p.publisher.SendContentBlockStop(index); err != nil {
			return nil, ctxerrors.Wrap(err, "close interrupted content block")
		}
	}

	projection := newWorkflowStreamProjection(ctx, p.publisher, position.next)
	p.adapter = elelemstream.New(projection.publisher)

	return projection, nil
}

func (p *preparedTurn) readWorkflowStreamPosition(
	ctx context.Context,
) (workflowStreamPosition, error) {
	position := workflowStreamPosition{open: make(map[int]struct{})}

	options := session.ListEventsOptions{
		TurnID: p.lease.TurnID, Limit: session.MaximumPageLimit,
		EventTypes: []string{
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockStop,
		},
	}
	for {
		page, err := p.turn.store.ListEvents(ctx, p.lease.SessionID, options)
		if err != nil {
			return position, ctxerrors.Wrap(err, "read stream block page")
		}

		for _, event := range page.Items {
			if event.RequestID != p.turn.requestID ||
				event.ParentToolCallID != "" {
				continue
			}

			if err := position.observe(event); err != nil {
				return position, ctxerrors.Wrap(err,
					"restore stream block state")
			}
		}

		if !page.HasMore {
			return position, nil
		}

		if len(page.Items) == 0 || options.Offset > math.MaxInt-page.Limit {
			return position, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"stream pagination cannot advance")
		}

		options.Offset += page.Limit
	}
}

func (p *workflowStreamPosition) observe(event *models.Event) error {
	index, err := workflowStreamBlockIndex([]byte(event.PayloadJSON))
	if err != nil {
		return ctxerrors.Wrap(err, "decode retained stream block")
	}

	p.next = max(p.next, index+1)

	if event.EventType == essessey.EventTypeContentBlockStart {
		p.open[index] = struct{}{}

		return nil
	}

	delete(p.open, index)

	return nil
}
