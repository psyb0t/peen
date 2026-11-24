package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// The SDK serializes errors verbatim. Keep internal causes out of results.
func toolFailure(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: failureMessage(err)}},
	}
}

func failureMessage(err error) string {
	var conflict *session.AgentSpaceConflictError
	if errors.As(err, &conflict) {
		return conflict.Error()
	}

	switch {
	case errors.Is(err, context.Canceled):
		return failureCancelled
	case errors.Is(err, agent.ErrTurnTimeout),
		errors.Is(err, context.DeadlineExceeded):
		return failureTimeout
	case errors.Is(err, commerr.ErrPermissionDenied):
		return failureDenied
	case errors.Is(err, commerr.ErrNotFound):
		return failureMissing
	case busyFailure(err):
		return failureBusy
	case invalidFailure(err):
		return failureInvalid
	default:
		return failureInternal
	}
}

func busyFailure(err error) bool {
	return errors.Is(err, session.ErrSessionBusy) ||
		errors.Is(err, commerr.ErrConflict) ||
		errors.Is(err, commerr.ErrLockHeld)
}

func invalidFailure(err error) bool {
	return errors.Is(err, commerr.ErrValidationFailed) ||
		errors.Is(err, agent.ErrOutputSchemaInvalid) ||
		errors.Is(err, commerr.ErrOutOfRange)
}
