package mcptools

import "errors"

var (
	// ErrInvalidConfig marks an mcp.json file or server entry Peen refuses.
	ErrInvalidConfig = errors.New("invalid MCP configuration")
	// ErrServerUnavailable marks a server that could not be started.
	ErrServerUnavailable = errors.New("MCP server unavailable")
)
