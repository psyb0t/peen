package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/peen/internal/pkg/control"
)

func (t tools) listFiles(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listFilesInput,
) (*mcp.CallToolResult, control.WorkspaceFileListing, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileListing{}, nil
	}

	result, err := t.dependencies.Files.ListWorkspaceFiles(ctx, id, input.Path)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileListing{}, nil
	}

	return nil, result, nil
}

func (t tools) readFile(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input fileInput,
) (*mcp.CallToolResult, control.WorkspaceFileContent, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileContent{}, nil
	}

	result, err := t.dependencies.Files.ReadWorkspaceFile(ctx, id, input.Path)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileContent{}, nil
	}

	return nil, result, nil
}

func (t tools) writeFile(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input writeFileInput,
) (*mcp.CallToolResult, control.WorkspaceFileEntry, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileEntry{}, nil
	}

	result, err := t.dependencies.Files.WriteWorkspaceFile(ctx, id,
		input.Path, input.Content, input.Dirs)
	if err != nil {
		return toolFailure(err), control.WorkspaceFileEntry{}, nil
	}

	return nil, result, nil
}

func (t tools) deleteFile(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input fileInput,
) (*mcp.CallToolResult, deleteResult, error) {
	id, err := t.sessionID(ctx, input.Session)
	if err != nil {
		return toolFailure(err), deleteResult{}, nil
	}

	if err := t.dependencies.Files.DeleteWorkspaceFile(ctx, id,
		input.Path); err != nil {
		return toolFailure(err), deleteResult{}, nil
	}

	return nil, deleteResult{Deleted: true}, nil
}
