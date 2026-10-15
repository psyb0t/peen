package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/essessey/elelemstream"
)

func (p *preparedTurn) workflowPhaseToolAdapter(
	ctx context.Context,
	input workflowToolPhaseInput,
) (*elelemstream.Adapter, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Tool.Round.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode phase tool stream")
	}

	return p.workflowToolAdapterForConversation(ctx, input.Tool, conversation,
		input.State.StreamAttempt)
}
