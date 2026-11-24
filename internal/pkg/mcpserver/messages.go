package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
)

func (t tools) sendMessage(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input sendInput,
) (*mcp.CallToolResult, sendResult, error) {
	request, err := t.messageRequest(ctx, input)
	if err != nil {
		return toolFailure(err), sendResult{}, nil
	}

	answer, err := t.dependencies.Runner.Run(ctx, request)
	if err != nil {
		return toolFailure(err), sendResult{}, nil
	}

	if answer == nil || answer.SessionID == uuid.Nil ||
		(len(answer.Output) != 0 &&
			!json.Valid(answer.Output)) {
		return toolFailure(commerr.ErrInvalidState), sendResult{}, nil
	}

	result := sendResult{
		SessionID: answer.SessionID.String(), Text: answer.Text,
		State: answer.State, Output: answer.Output,
	}
	if answer.OutputError != "" {
		result.OutputError = answer.OutputError
		result.Output = nil
	}

	return nil, result, nil
}

func (t tools) cancelTurn(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input sessionInput,
) (*mcp.CallToolResult, cancelResult, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), cancelResult{}, nil
	}

	cancelled, err := t.dependencies.Turns.CancelSessionTurn(ctx, id)
	if err != nil {
		return toolFailure(err), cancelResult{}, nil
	}

	return nil, cancelResult{Cancelled: cancelled}, nil
}

var _ Runner = (*control.Conversation)(nil)
