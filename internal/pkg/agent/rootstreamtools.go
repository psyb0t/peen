package agent

import (
	"context"
	"encoding/json"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey/elelemstream"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const workflowContentBlocksPerTool = 2

type workflowToolStream struct {
	Base  int `json:"base"`
	Count int `json:"count"`
}

func (p *preparedTurn) workflowToolAdapter(
	ctx context.Context,
	input workflowToolInput,
) (*elelemstream.Adapter, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Round.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode saved tool stream slots")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"tool stream requires an activity lease")
	}

	return p.workflowToolAdapterForConversation(ctx, input, conversation,
		lease.Attempt)
}

func (p *preparedTurn) workflowToolAdapterForConversation(
	ctx context.Context,
	input workflowToolInput,
	conversation workflowConversation,
	attempt int64,
) (*elelemstream.Adapter, error) {
	base, err := conversation.Stream.offset(input.Index, attempt)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore tool stream offset")
	}

	calls := workflowStreamToolCalls(conversation.Messages)
	if len(calls) != conversation.Stream.Count {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved stream slots differ from the model tool batch")
	}

	projection := newWorkflowStreamProjection(ctx, p.publisher, base)

	adapter := elelemstream.New(projection.publisher)
	if err := adapter.OnRoundStart(ctx, &elelem.RoundEvent{}); err != nil {
		return nil, ctxerrors.Wrap(err, "initialize restored tool stream")
	}

	if err := adapter.OnAssistantMessage(ctx, elelem.Message{
		ToolCalls: calls,
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "restore tool batch slots")
	}

	return adapter, nil
}

func workflowStreamToolCalls(messages []elelem.Message) []elelem.ToolCall {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == elelem.RoleAssistant {
			return messages[index].ToolCalls
		}
	}

	return nil
}

func (s *workflowToolStream) offset(index int, attempt int64) (int, error) {
	if s == nil || s.Base < 0 || s.Count <= 0 ||
		index < 0 || index >= s.Count || attempt < 1 {
		return 0, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved tool stream slots are invalid")
	}

	if s.Count > math.MaxInt/workflowContentBlocksPerTool {
		return 0, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool stream slot count exceeds integer capacity")
	}

	slots := s.Count * workflowContentBlocksPerTool
	if attempt > int64((math.MaxInt-s.Base)/slots) {
		return 0, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"tool stream attempt exceeds integer capacity")
	}

	return s.Base + int(attempt-1)*slots, nil
}
