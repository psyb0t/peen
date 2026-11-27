package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// Templates rather than one resource per session: the SDK lists only
// registered resources, and that list is shared by every client.
func (t tools) registerResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         uriSessions,
		Name:        resourceSessions,
		MIMEType:    mimeJSON,
		Description: describeSessions,
	}, t.readSessions)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: templateSession,
		Name:        resourceSession,
		MIMEType:    mimeJSON,
		Description: describeSession,
	}, t.readSession)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: templateMessages,
		Name:        resourceMessages,
		MIMEType:    mimeJSON,
		Description: describeMessages,
	}, t.readSessionMessages)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: templateFiles,
		Name:        resourceFile,
		Description: describeFile,
	}, t.readWorkspaceResource)
}

func (t tools) readSessions(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	uri := request.Params.URI
	limit := int32(session.MaximumPageLimit)

	page, err := t.dependencies.Sessions.ListSessions(ctx,
		api.ListSessionsParams{Limit: &limit})
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	visible, err := t.visibleSessions(ctx, page.Items)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	index := sessionIndex{
		Items: make([]sessionIndexItem, 0, len(visible)),
		Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore,
	}
	for _, item := range visible {
		index.Items = append(index.Items,
			sessionIndexItem{Session: item, URI: sessionURI(item.Id)})
	}

	return jsonResource(ctx, uri, index)
}

func (t tools) readSession(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	uri := request.Params.URI

	id, err := t.sessionTarget(ctx, uri, "")
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	stored, err := t.dependencies.Reader.Session(ctx, id)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	if stored == nil {
		return nil, resourceFailure(ctx, uri, commerr.ErrInvalidState)
	}

	return jsonResource(ctx, uri, stored)
}

func (t tools) readSessionMessages(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	uri := request.Params.URI

	id, err := t.sessionTarget(ctx, uri, uriMessagesSuffix)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	page, err := t.latestMessages(ctx, id,
		t.dependencies.MaxResourceMessages)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	return jsonResource(ctx, uri, page)
}

// sessionTarget checks a session URI that must end in suffix after its ID.
func (t tools) sessionTarget(
	ctx context.Context,
	uri, suffix string,
) (uuid.UUID, error) {
	id, rest, err := t.resourceSession(ctx, uri)
	if err != nil {
		return uuid.Nil, err
	}

	if rest != suffix {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrNotFound,
			"MCP session URI has an unexpected suffix")
	}

	return id, nil
}

// readWorkspaceResource answers a directory with its listing and a file with
// its bytes, as text when they are UTF-8 without NUL and as a blob otherwise.
func (t tools) readWorkspaceResource(
	ctx context.Context,
	request *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	uri := request.Params.URI

	id, rest, err := t.resourceSession(ctx, uri)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	encodedPath, ok := strings.CutPrefix(rest, uriFilesSegment)
	if !ok {
		return nil, resourceFailure(ctx, uri, commerr.ErrNotFound)
	}

	relative, err := resourcePath(encodedPath)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	listing, err := t.dependencies.Files.ListWorkspaceFiles(ctx, id, relative)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	if listing.Type == api.WorkspaceFileTypeDirectory {
		return jsonResource(ctx, uri, listing)
	}

	content, err := t.dependencies.Files.ReadWorkspaceFile(ctx, id, relative)
	if err != nil {
		return nil, resourceFailure(ctx, uri, err)
	}

	data, err := base64.StdEncoding.DecodeString(content.ContentBase64)
	if err != nil {
		return nil, resourceFailure(ctx, uri,
			ctxerrors.Wrap(err, "decode MCP workspace file"))
	}

	return fileResource(uri, data), nil
}

// resourceSession takes the session ID from a session URI, decodes it once,
// and passes it through the same policy check as every tool. It returns the
// raw rest of the URI after the ID.
func (t tools) resourceSession(
	ctx context.Context,
	uri string,
) (uuid.UUID, string, error) {
	tail, ok := strings.CutPrefix(uri, uriSessionPrefix)
	if !ok {
		return uuid.Nil, "", ctxerrors.Wrap(commerr.ErrNotFound,
			"MCP resource is not a session URI")
	}

	encodedID, rest := tail, ""
	if index := strings.IndexByte(tail, '/'); index >= 0 {
		encodedID, rest = tail[:index], tail[index:]
	}

	decoded, err := url.PathUnescape(encodedID)
	if err != nil {
		return uuid.Nil, "", ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP resource session ID is not escaped correctly")
	}

	id, err := t.sessionID(ctx, decoded)
	if err != nil {
		return uuid.Nil, "", err
	}

	return id, rest, nil
}

// resourcePath decodes a template path exactly once. It refuses a path that
// is not clean and relative, and one that a second decode would turn into
// such a path, so neither %2e%2e nor %252e%252e reaches the file service.
func resourcePath(encoded string) (string, error) {
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP resource path is not escaped correctly")
	}

	twice, err := url.PathUnescape(decoded)
	if !cleanRelativePath(decoded) ||
		(err == nil && !cleanRelativePath(twice)) {
		return "", ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"MCP resource path leaves the workspace or is not clean")
	}

	return decoded, nil
}

// cleanRelativePath accepts the workspace root as "" and otherwise only a
// clean relative path with no "." or ".." element.
func cleanRelativePath(value string) bool {
	return value == "" || (value != "." && fs.ValidPath(value) &&
		!strings.ContainsRune(value, 0))
}

func jsonResource(
	ctx context.Context,
	uri string,
	value any,
) (*mcp.ReadResourceResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, resourceFailure(ctx, uri,
			ctxerrors.Wrap(err, "encode MCP resource"))
	}

	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: uri, MIMEType: mimeJSON, Text: string(encoded),
	}}}, nil
}

func fileResource(uri string, data []byte) *mcp.ReadResourceResult {
	contents := &mcp.ResourceContents{URI: uri}
	if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
		contents.MIMEType = mimeText
		contents.Text = string(data)
	} else {
		contents.MIMEType = mimeBinary
		contents.Blob = data
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{contents},
	}
}

// resourceFailure maps a cause to a JSON-RPC error with a fixed message. The
// SDK sends a handler's error text verbatim, so the cause is only logged.
func resourceFailure(ctx context.Context, uri string, err error) error {
	ctxscope.GetLogger(ctx).Debug("MCP resource read failed", "error", err)

	switch {
	case errors.Is(err, commerr.ErrNotFound):
		//nolint:wrapcheck // The SDK sends this JSON-RPC error as it is.
		return mcp.ResourceNotFoundError(uri)
	case errors.Is(err, commerr.ErrPermissionDenied):
		return &jsonrpc.Error{
			Code: jsonrpc.CodeInvalidParams, Message: failureDenied,
		}
	case invalidFailure(err):
		return &jsonrpc.Error{
			Code: jsonrpc.CodeInvalidParams, Message: failureResource,
		}
	default:
		return &jsonrpc.Error{
			Code: jsonrpc.CodeInternalError, Message: failureInternal,
		}
	}
}

func sessionURI(id uuid.UUID) string {
	return uriSessionPrefix + id.String()
}

func sessionLink(id uuid.UUID) *mcp.ResourceLink {
	return &mcp.ResourceLink{
		URI: sessionURI(id), Name: resourceSession, MIMEType: mimeJSON,
	}
}

func sessionLinks(id uuid.UUID) []mcp.Content {
	return []mcp.Content{sessionLink(id), &mcp.ResourceLink{
		URI:      sessionURI(id) + uriMessagesSuffix,
		Name:     resourceMessages,
		MIMEType: mimeJSON,
	}}
}

// linkedResult keeps the JSON text fallback the SDK would add on its own,
// then appends resource links to the result content.
func linkedResult(
	value any,
	links []mcp.Content,
) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode MCP tool result")
	}

	content := make([]mcp.Content, 0, len(links)+1)
	content = append(content, &mcp.TextContent{Text: string(encoded)})
	content = append(content, links...)

	return &mcp.CallToolResult{Content: content}, nil
}
