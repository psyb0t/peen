package run

import (
	"context"
	"os"
	"runtime/debug"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/mcptools"
)

// startMCP reads this session's MCP configuration and starts its servers in
// the background, so the worker serves the controller while a slow server
// starts. The first tool set built waits for the start to finish. With
// PEEN_MCP off it returns nil, which the runtime treats as no MCP.
func startMCP(
	ctx context.Context,
	deployment peenconfig.Config,
	workspace string,
) (*mcptools.Manager, error) {
	if !deployment.MCP {
		return nil, nil //nolint:nilnil // No manager is how MCP is off.
	}

	servers, failures := mcptools.Load(mcptools.LoadOptions{
		ConfigDirectory:  deployment.ConfigDirectory,
		Workspace:        workspace,
		WorkspaceEnabled: deployment.WorkspaceMCP,
		SecretEnv:        deployment.MCPEnvNames(),
	})

	// A stdio server starts from the same environment as the agent's
	// commands, without Peen's secrets, plus its own env and envFrom.
	environment, err := deployment.CommandEnvironment(os.Environ())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the MCP server environment")
	}

	manager := mcptools.New(mcptools.Options{
		Servers:         servers,
		Failures:        failures,
		BaseEnvironment: environment,
		LookupEnv:       os.LookupEnv,
		StartTimeout:    deployment.MCPStartTimeout,
	})

	if !manager.Configured() {
		return manager, nil
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(ctx).Error("MCP start panicked",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()

		manager.Start(ctx)
	}()

	return manager, nil
}

// stopMCP stops the session's MCP servers when the worker exits.
func stopMCP(ctx context.Context, manager *mcptools.Manager) {
	if err := manager.Close(); err != nil {
		ctxscope.GetLogger(ctx).Warn("MCP servers did not stop cleanly",
			"reason", "mcp_close_failed",
			"err", err,
		)
	}
}
