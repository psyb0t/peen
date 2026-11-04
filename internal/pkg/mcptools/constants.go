// Package mcptools connects a session's worker to the MCP servers the
// operator configures, through elelem's mcp package.
//
// Servers are read from .agents/mcp.json in PEEN_CONFIG_DIR and, when the
// operator allows it, in the session workspace. A stdio server runs as a
// child of the worker, with the agent's access. A Streamable HTTP server is
// reached from the worker's network. Only tools are supported.
package mcptools

import (
	"time"

	"github.com/psyb0t/elelem/mcp"
)

const (
	// FileName is the MCP configuration file inside a layer's .agents
	// directory.
	FileName = "mcp.json"

	agentsDirectoryName = ".agents"

	// maxServerNameLength bounds a server name, so the tool names built from
	// it keep room for the tool.
	maxServerNameLength = mcp.MaxServerNameLength

	// maxConfigBytes bounds one mcp.json file.
	maxConfigBytes = 1 << 20
	// maxServers bounds the servers one session may configure.
	maxServers = 32

	clientName = "peen"

	// DefaultStartTimeout bounds starting one server and listing its tools.
	DefaultStartTimeout = 30 * time.Second

	httpSchemePrefix  = "http://"
	httpsSchemePrefix = "https://"
)

// Transport is how Peen reaches an MCP server.
type Transport = string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Source is the layer an MCP server was configured in.
type Source = string

const (
	SourceConfig    Source = "config"
	SourceWorkspace Source = "workspace"
)

// State is what happened when Peen started a server.
type State = string

const (
	StateReady  State = "ready"
	StateFailed State = "failed"
)
