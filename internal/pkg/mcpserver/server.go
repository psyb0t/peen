package mcpserver

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// New registers Peen's controller tools. HTTP authentication, origin policy,
// transport limits and request cancellation belong to the mounting server.
func New(dependencies Dependencies) (*mcp.Server, error) {
	if dependencies.Runner == nil || dependencies.Sessions == nil ||
		dependencies.Reader == nil || dependencies.Turns == nil ||
		dependencies.Files == nil || dependencies.MaxOutputSchemaBytes < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP tool dependencies are invalid")
	}

	if dependencies.MaxOutputSchemaBytes == 0 {
		dependencies.MaxOutputSchemaBytes = defaultMaxOutputSchemaBytes
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name: serverName, Version: serverVersion,
	}, nil)
	tools := tools{dependencies: dependencies}
	tools.registerMessages(server)
	tools.registerReads(server)
	tools.registerFiles(server)

	return server, nil
}

func toolDefinition(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Description: description,
		// UUID and RawMessage marshal differently from SDK inference.
		OutputSchema: json.RawMessage(objectOutputSchema),
	}
}

func (t tools) registerMessages(server *mcp.Server) {
	mcp.AddTool(server, toolDefinition(toolSendMessage,
		"Run a message in one session or workspace and wait for its turn."),
		t.sendMessage)
	mcp.AddTool(server, toolDefinition(toolCancelTurn,
		"Cancel the active turn in a regular workspace session."),
		t.cancelTurn)
}

func (t tools) registerReads(server *mcp.Server) {
	mcp.AddTool(server, toolDefinition(toolListSessions,
		"List allowed workspace sessions with bounded pagination."),
		t.listSessions)
	mcp.AddTool(server, toolDefinition(toolGetSession,
		"Read one regular workspace session's stored state."),
		t.getSession)
	mcp.AddTool(server, toolDefinition(toolReadMessages,
		"Read the last bounded messages in oldest-to-newest order."),
		t.readMessages)
	mcp.AddTool(server, toolDefinition(toolListModels,
		"List configured model IDs accepted by sendMessage."),
		t.listModels)
}

func (t tools) registerFiles(server *mcp.Server) {
	mcp.AddTool(server, toolDefinition(toolListFiles,
		"List a workspace directory or inspect a relative path."),
		t.listFiles)
	mcp.AddTool(server, toolDefinition(toolReadFile,
		"Read bounded workspace file bytes encoded as base64."),
		t.readFile)
	mcp.AddTool(server, toolDefinition(toolWriteFile,
		"Atomically write base64 bytes to a relative workspace path."),
		t.writeFile)
	mcp.AddTool(server, toolDefinition(toolDeleteFile,
		"Delete one workspace file, symlink, or empty directory."),
		t.deleteFile)
}
