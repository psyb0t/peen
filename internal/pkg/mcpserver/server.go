package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// New registers Peen's controller tools, resources and completion. HTTP
// authentication, origin policy, transport limits and request cancellation
// belong to the mounting server.
func New(dependencies Dependencies) (*mcp.Server, error) {
	dependencies, err := withDefaults(dependencies)
	if err != nil {
		return nil, err
	}

	tools := tools{dependencies: dependencies}
	server := mcp.NewServer(&mcp.Implementation{
		Name: serverName, Version: serverVersion,
	}, &mcp.ServerOptions{
		Instructions:      serverInstructions,
		Capabilities:      serverCapabilities(),
		CompletionHandler: tools.complete,
		SetCacheable:      privateUncached,
	})
	tools.registerMessages(server)
	tools.registerReads(server)
	tools.registerFiles(server)
	tools.registerResources(server)

	return server, nil
}

func withDefaults(dependencies Dependencies) (Dependencies, error) {
	if !dependencies.complete() || dependencies.MaxOutputSchemaBytes < 0 ||
		dependencies.MaxResourceMessages < 0 ||
		dependencies.MaxResourceMessages > session.MaximumPageLimit {
		return dependencies, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP tool dependencies are invalid")
	}

	if dependencies.MaxOutputSchemaBytes == 0 {
		dependencies.MaxOutputSchemaBytes = defaultMaxOutputSchemaBytes
	}

	if dependencies.MaxResourceMessages == 0 {
		dependencies.MaxResourceMessages = defaultMaxResourceMessages
	}

	return dependencies, nil
}

// serverCapabilities replaces the SDK default, which advertises the
// deprecated logging feature. Nothing here changes after startup, so no list
// sends change notifications, and resources offer no subscriptions.
func serverCapabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{
		Tools:       &mcp.ToolCapabilities{},
		Resources:   &mcp.ResourceCapabilities{},
		Completions: &mcp.CompletionCapabilities{},
	}
}

// privateUncached keeps transcripts and files out of shared caches and makes
// every list and read result stale at once.
func privateUncached(_ context.Context, _ mcp.Request, cache *mcp.Cacheable) {
	cache.TTLMs = 0
	cache.CacheScope = cacheScopePrivate
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
