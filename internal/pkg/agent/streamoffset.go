package agent

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/essessey"
)

const (
	workflowStreamIndexField = "index"
	workflowStreamIndexRadix = 10
)

type workflowStreamProjection struct {
	mutex     sync.Mutex
	next      int
	base      int
	publisher *essessey.Publisher
	target    *essessey.Publisher
}

func newWorkflowStreamProjection(
	ctx context.Context,
	target *essessey.Publisher,
	base int,
) *workflowStreamProjection {
	projection := &workflowStreamProjection{
		base: base, next: base, target: target,
	}
	projection.publisher = essessey.NewPublisher(ctx, projection)

	return projection
}

func (p *workflowStreamProjection) Emit(
	ctx context.Context,
	event essessey.Event,
) error {
	shifted, err := offsetWorkflowStreamEvent(event, p.base)
	if err != nil {
		return ctxerrors.Wrap(err, "translate workflow stream index")
	}

	if err := p.target.Publish(shifted.Event, shifted.Data); err != nil {
		return ctxerrors.Wrap(err, "publish workflow stream event")
	}

	if shifted.Event != essessey.EventTypeContentBlockStart {
		return nil
	}

	index, err := workflowStreamBlockIndex(shifted.Data)
	if err != nil {
		return ctxerrors.Wrap(err, "read published stream index")
	}

	p.mutex.Lock()
	p.next = max(p.next, index+1)
	p.mutex.Unlock()
	ctxscope.GetLogger(ctx).Debug("workflow stream block recorded",
		"block_index", index, "block_offset", p.base)

	return nil
}

func (p *workflowStreamProjection) nextIndex() int {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.next
}

func offsetWorkflowStreamEvent(
	event essessey.Event,
	base int,
) (essessey.Event, error) {
	if base < 0 {
		return event, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"workflow stream offset is negative")
	}

	if !isWorkflowBlockEvent(event.Event) {
		return event, nil
	}

	index, err := workflowStreamBlockIndex(event.Data)
	if err != nil {
		return event, ctxerrors.Wrap(err, "read relative stream index")
	}

	if index >= math.MaxInt-base {
		return event, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"workflow stream index exceeds integer capacity")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &fields); err != nil {
		return event, ctxerrors.Wrap(err, "decode stream index fields")
	}

	fields[workflowStreamIndexField] = strconv.AppendInt(nil,
		int64(index+base), workflowStreamIndexRadix)

	encoded, err := json.Marshal(fields)
	if err != nil {
		return event, ctxerrors.Wrap(err, "encode shifted stream event")
	}

	event.Data = encoded

	return event, nil
}

func workflowStreamBlockIndex(data json.RawMessage) (int, error) {
	var block struct {
		Index *int `json:"index"`
	}
	if err := json.Unmarshal(data, &block); err != nil {
		return 0, ctxerrors.Wrap(err, "decode content block index")
	}

	if block.Index == nil || *block.Index < 0 || *block.Index == math.MaxInt {
		return 0, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"content block index is missing or invalid")
	}

	return *block.Index, nil
}

func isWorkflowBlockEvent(event essessey.EventType) bool {
	return event == essessey.EventTypeContentBlockStart ||
		event == essessey.EventTypeContentBlockDelta ||
		event == essessey.EventTypeContentBlockStop
}
