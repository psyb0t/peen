package mcpserver

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// complete suggests template arguments. It reveals no more than the tools:
// session IDs that listSessions shows, and entries of a directory inside a
// permitted workspace. Anything else completes to nothing.
func (t tools) complete(
	ctx context.Context,
	request *mcp.CompleteRequest,
) (*mcp.CompleteResult, error) {
	params := request.Params
	if params == nil || params.Ref == nil ||
		params.Ref.Type != referenceResource {
		return completion(nil, false), nil
	}

	switch params.Argument.Name {
	case argumentSessionID:
		if !slices.Contains([]string{
			templateSession, templateMessages, templateFiles,
		}, params.Ref.URI) {
			return completion(nil, false), nil
		}

		return t.completeSessions(ctx, params.Argument.Value)
	case argumentPath:
		if params.Ref.URI != templateFiles {
			return completion(nil, false), nil
		}

		return t.completePaths(ctx, params)
	default:
		return completion(nil, false), nil
	}
}

func (t tools) completeSessions(
	ctx context.Context,
	value string,
) (*mcp.CompleteResult, error) {
	prefix := strings.ToLower(value)
	values := make([]string, 0, maxCompletionValues)
	limit := int32(session.MaximumPageLimit)

	for offset := int32(0); offset < maxCompletionScanned; offset += limit {
		page, err := t.dependencies.Sessions.ListSessions(ctx,
			api.ListSessionsParams{Limit: &limit, Offset: &offset})
		if err != nil {
			return completionFailure(ctx, err)
		}

		matching := make([]api.Session, 0, len(page.Items))
		for _, item := range page.Items {
			if strings.HasPrefix(item.Id.String(), prefix) {
				matching = append(matching, item)
			}
		}

		visible, err := t.visibleSessions(ctx, matching)
		if err != nil {
			return completionFailure(ctx, err)
		}

		for _, item := range visible {
			if len(values) == maxCompletionValues {
				return completion(values, true), nil
			}

			values = append(values, item.Id.String())
		}

		if !page.HasMore {
			break
		}
	}

	return completion(values, false), nil
}

// completePaths lists the parent directory of the typed path. The typed
// value is the raw template variable, not percent-encoded.
func (t tools) completePaths(
	ctx context.Context,
	params *mcp.CompleteParams,
) (*mcp.CompleteResult, error) {
	if params.Context == nil {
		return completion(nil, false), nil
	}

	id, err := t.sessionID(ctx, params.Context.Arguments[argumentSessionID])
	if err != nil {
		return completionFailure(ctx, err)
	}

	parent, prefix := splitCompletionPath(params.Argument.Value)
	if !cleanRelativePath(parent) {
		return completion(nil, false), nil
	}

	listing, err := t.dependencies.Files.ListWorkspaceFiles(ctx, id, parent)
	if err != nil {
		return completionFailure(ctx, err)
	}

	if listing.Type != api.WorkspaceFileTypeDirectory {
		return completion(nil, false), nil
	}

	values := make([]string, 0, maxCompletionValues)

	for _, entry := range listing.Entries {
		if !strings.HasPrefix(entry.Name, prefix) {
			continue
		}

		if len(values) == maxCompletionValues {
			return completion(values, true), nil
		}

		values = append(values, path.Join(parent, entry.Name))
	}

	return completion(values, false), nil
}

func splitCompletionPath(value string) (string, string) {
	index := strings.LastIndexByte(value, '/')
	if index < 0 {
		return "", value
	}

	return value[:index], value[index+1:]
}

// completionFailure answers a refused, malformed or unknown session or path
// with no values, so completion cannot tell them apart. Other failures get a
// fixed JSON-RPC error.
func completionFailure(
	ctx context.Context,
	err error,
) (*mcp.CompleteResult, error) {
	ctxscope.GetLogger(ctx).Debug("MCP completion failed", "error", err)

	if errors.Is(err, commerr.ErrPermissionDenied) ||
		errors.Is(err, commerr.ErrNotFound) || invalidFailure(err) {
		return completion(nil, false), nil
	}

	return nil, &jsonrpc.Error{
		Code: jsonrpc.CodeInternalError, Message: failureInternal,
	}
}

func completion(values []string, more bool) *mcp.CompleteResult {
	if values == nil {
		values = []string{}
	}

	return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{
		Values: values, HasMore: more,
	}}
}
