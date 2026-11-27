package mcpserver

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
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

	page.Items, err = t.visibleSessions(ctx, page.Items)
	if err != nil {
		return toolFailure(err), api.SessionPage{}, nil
	}

	links := make([]mcp.Content, 0, len(page.Items))
	for _, item := range page.Items {
		links = append(links, sessionLink(item.Id))
	}

	result, err := linkedResult(page, links)
	if err != nil {
		return toolFailure(err), api.SessionPage{}, nil
	}

	return result, page, nil
}

// visibleSessions drops sessions that current policy refuses or that went
// away after paging, as listSessions does.
func (t tools) visibleSessions(
	ctx context.Context,
	items []api.Session,
) ([]api.Session, error) {
	visible := make([]api.Session, 0, len(items))
	for _, item := range items {
		err := t.dependencies.Sessions.ValidateWorkspaceSession(ctx, item.Id)
		if errors.Is(err, commerr.ErrPermissionDenied) ||
			errors.Is(err, commerr.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, ctxerrors.Wrap(err, "validate listed MCP session")
		}

		visible = append(visible, item)
	}

	return visible, nil
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

	stored, err := t.dependencies.Reader.Session(ctx, id)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	if stored == nil {
		return toolFailure(commerr.ErrInvalidState), nil, nil
	}

	result, err := linkedResult(stored, sessionLinks(id))
	if err != nil {
		return toolFailure(err), nil, nil
	}

	return result, stored, nil
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

	page, err := t.latestMessages(ctx, id, limit)
	if err != nil {
		return toolFailure(err), nil, nil
	}

	return nil, page, nil
}

// latestMessages reads the newest limit messages, oldest first.
func (t tools) latestMessages(
	ctx context.Context,
	id uuid.UUID,
	limit int32,
) (*api.MessagePage, error) {
	order := api.ListMessagesParamsOrderDesc

	page, err := t.dependencies.Reader.ListMessages(ctx,
		api.ListMessagesParams{XSessionID: id, Limit: &limit, Order: &order})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list MCP session messages")
	}

	if page == nil {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"MCP message page is missing")
	}

	result := *page
	result.Items = slices.Clone(page.Items)
	slices.Reverse(result.Items)

	return &result, nil
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
