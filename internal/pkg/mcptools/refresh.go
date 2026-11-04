package mcptools

import (
	"context"
	"reflect"

	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

// Refresh brings the servers up to date at the start of a turn, so a turn
// sees the current configuration and tool lists. It starts the servers if
// nothing has. When the configuration changed it restarts every server. A
// server that ran and stopped restarts. A server that never started is tried
// again only when the configuration changes, so a server that cannot start
// does not cost every turn its start timeout. Every running server lists its
// tools again, which picks up a server's own tool changes.
func (m *Manager) Refresh(ctx context.Context) {
	if m == nil {
		return
	}

	m.Start(ctx)

	var (
		servers  []Server
		failures []Status
	)

	if m.options.Reload != nil {
		servers, failures = m.options.Reload()
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return
	}

	reloaded := m.options.Reload != nil
	if reloaded && !reflect.DeepEqual(servers, m.options.Servers) {
		m.replaceServers(ctx, servers, failures)

		return
	}

	if reloaded {
		m.options.Failures = failures
	}

	m.refreshServers(ctx)
}

// replaceServers stops every server and starts the new configuration. The
// caller holds the mutex.
func (m *Manager) replaceServers(
	ctx context.Context,
	servers []Server,
	failures []Status,
) {
	logger := runtimecontext.Logger(ctx)
	logger.Info("MCP configuration changed, restarting servers",
		"server_count", len(servers),
	)

	if err := closeServers(m.servers); err != nil {
		logger.Warn("MCP servers did not stop cleanly",
			"reason", "mcp_reload_close_failed",
			"err", err,
		)
	}

	m.options.Servers = servers
	m.options.Failures = failures
	m.servers = m.connectAll(ctx, servers)
}

// refreshServers restarts each server that stopped and lists the tools of
// each running one again. The caller holds the mutex.
func (m *Manager) refreshServers(ctx context.Context) {
	logger := runtimecontext.Logger(ctx)

	for index, server := range m.servers {
		if server.connection == nil {
			continue
		}

		if server.connection.Err() != nil {
			logger.Info("MCP server stopped, restarting it",
				"server", server.config.Name,
			)

			if err := server.connection.Close(); err != nil {
				logger.Debug("MCP server close failed",
					"server", server.config.Name, "err", err)
			}

			m.servers[index] = m.connect(ctx, server.config)

			continue
		}

		startCtx, cancel := context.WithTimeout(ctx, m.options.StartTimeout)
		err := server.connection.RefreshTools(startCtx)

		cancel()

		if err != nil {
			logger.Warn("MCP server tools could not be listed again",
				"server", server.config.Name,
				"reason", "refresh_tools_failed",
				"err", err,
			)
		}
	}
}
