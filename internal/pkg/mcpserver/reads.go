package mcpserver

import (
	"context"
	"errors"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

func (t tools) listSessions(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listSessionsInput,
) (*mcp.CallToolResult, api.SessionPage, error) {
	limit, err := pageLimit(input.Limit)
	if err != nil {
		return toolFailure(err), api.SessionPage{}, nil
	}

	if input.Offset != nil && *input.Offset < 0 {
		return toolFailure(commerr.ErrValidationFailed), api.SessionPage{}, nil
	}

	page, err := t.dependencies.Sessions.ListSessions(ctx,
		api.ListSessionsParams{Limit: &limit, Offset: input.Offset})
	if err != nil {
		return toolFailure(err), api.SessionPage{}, nil
	}

	visible := make([]api.Session, 0, len(page.Items))
	for _, item := range page.Items {
		err := t.dependencies.Sessions.ValidateWorkspaceSession(ctx, item.Id)
		if errors.Is(err, commerr.ErrPermissionDenied) ||
			errors.Is(err, commerr.ErrNotFound) {
			continue
		}

		if err != nil {
			return toolFailure(err), api.SessionPage{}, nil
		}

		visible = append(visible, item)
	}

	page.Items = visible

	return nil, page, nil
}

func (t tools) getSession(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input sessionInput,
) (*mcp.CallToolResult, *api.Session, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	result, err := t.dependencies.Reader.Session(ctx, id)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	if result == nil {
		return toolFailure(commerr.ErrInvalidState), nil, nil
	}

	return nil, result, nil
}

func (t tools) readMessages(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input readMessagesInput,
) (*mcp.CallToolResult, *api.MessagePage, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	limit, err := pageLimit(input.Limit)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	order := api.ListMessagesParamsOrderDesc

	page, err := t.dependencies.Reader.ListMessages(ctx,
		api.ListMessagesParams{XSessionID: id, Limit: &limit, Order: &order})
	if err != nil {
		return toolFailure(err), nil, nil
	}

	if page == nil {
		return toolFailure(commerr.ErrInvalidState), nil, nil
	}

	result := *page
	result.Items = slices.Clone(page.Items)
	slices.Reverse(result.Items)

	return nil, &result, nil
}

func (t tools) listModels(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ emptyInput,
) (*mcp.CallToolResult, api.ModelList, error) {
	if err := ctx.Err(); err != nil {
		return toolFailure(err), api.ModelList{}, nil
	}

	return nil, t.dependencies.Reader.ListModels(), nil
}
