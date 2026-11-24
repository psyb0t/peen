package controlapi

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/mcpserver"
)

func mcpServer(core *control.Core) (*mcp.Server, error) {
	if !core.Config.MCPServer {
		return nil, nil //nolint:nilnil // Nil disables the optional protocol.
	}

	conversation, err := control.NewConversation(core.Sessions,
		core.Turns, core.Feed)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create MCP conversation runner")
	}

	server, err := mcpserver.New(mcpserver.Dependencies{
		Runner:               conversation,
		Sessions:             core.Sessions,
		Reader:               core.Runtime,
		Turns:                core.Turns,
		Files:                core.Sessions,
		MaxOutputSchemaBytes: core.Config.MaxOutputSchemaBytes,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create MCP tool server")
	}

	return server, nil
}
