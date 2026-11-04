package mcptools

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	refreshWait = 30 * time.Second
	refreshPoll = 20 * time.Millisecond
)

// reloadable is a configuration a test can change between turns.
type reloadable struct {
	mutex   sync.Mutex
	servers []Server
	calls   int
}

func (r *reloadable) set(servers ...Server) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.servers = servers
}

func (r *reloadable) load() ([]Server, []Status) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.calls++

	return append([]Server(nil), r.servers...), nil
}

func fixtureServer(t *testing.T, name string) Server {
	t.Helper()

	return Server{
		Name: name, Source: SourceConfig, Transport: TransportStdio,
		Command: "bash", Args: []string{testServerPath(t)},
	}
}

func reloadingManager(t *testing.T, config *reloadable) *Manager {
	t.Helper()

	servers, failures := config.load()
	manager := New(Options{
		Servers:         servers,
		Failures:        failures,
		BaseEnvironment: os.Environ(),
		StartTimeout:    testStartTimeout,
		Reload:          config.load,
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })

	return manager
}

func toolNames(tools []elelem.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return names
}

func TestRefreshStartsServersAddedAfterTheWorkerStarted(t *testing.T) {
	t.Parallel()

	config := &reloadable{}
	manager := reloadingManager(t, config)

	manager.Refresh(t.Context())
	assert.False(t, manager.Configured())
	assert.Empty(t, manager.Tools())

	config.set(fixtureServer(t, "fixture"))
	manager.Refresh(t.Context())

	assert.True(t, manager.Configured())
	assert.Contains(t, toolNames(manager.Tools()), "mcp__fixture__echo")
}

func TestRefreshRestartsServersWhenTheConfigurationChanges(t *testing.T) {
	t.Parallel()

	config := &reloadable{}
	config.set(fixtureServer(t, "first"))
	manager := reloadingManager(t, config)
	manager.Start(t.Context())

	first := manager.Tools()
	require.Contains(t, toolNames(first), "mcp__first__echo")

	config.set(fixtureServer(t, "second"))
	manager.Refresh(t.Context())

	names := toolNames(manager.Tools())
	assert.Contains(t, names, "mcp__second__echo")
	assert.NotContains(t, names, "mcp__first__echo")

	_, err := first[0].Handler(t.Context(), elelem.ToolInput{
		Arguments: json.RawMessage(`{}`),
	})
	require.Error(t, err, "the replaced server stopped")
}

func TestRefreshRestartsAServerThatStopped(t *testing.T) {
	t.Parallel()

	config := &reloadable{}
	config.set(fixtureServer(t, "fixture"))
	manager := reloadingManager(t, config)
	manager.Start(t.Context())

	_, err := callToolOf(t, manager.Tools(), "mcp__fixture__exit")
	require.Error(t, err, "the server exits mid-call")

	require.Eventually(t, func() bool {
		return statusByName(manager.Status(t.Context()))["fixture"].State ==
			StateFailed
	}, refreshWait, refreshPoll)
	assert.Empty(t, manager.Tools(), "a stopped server offers no tools")

	manager.Refresh(t.Context())

	assert.Equal(t, StateReady,
		statusByName(manager.Status(t.Context()))["fixture"].State)
	assert.Equal(t, "mcp echo: hi",
		callTestTool(t, manager.Tools(), "mcp__fixture__echo",
			`{"message":"hi"}`).Content)
}

func TestRefreshDoesNotRetryAServerThatNeverStarted(t *testing.T) {
	t.Parallel()

	broken := Server{
		Name: "broken", Source: SourceConfig, Transport: TransportStdio,
		Command: "/does/not/exist",
	}
	config := &reloadable{}
	config.set(broken)
	manager := reloadingManager(t, config)
	manager.Start(t.Context())

	before := statusByName(manager.Status(t.Context()))["broken"]
	require.Equal(t, StateFailed, before.State)

	manager.Refresh(t.Context())

	after := statusByName(manager.Status(t.Context()))["broken"]
	assert.Equal(t, before, after)
}

func TestRefreshWithoutReloadKeepsTheConfiguration(t *testing.T) {
	t.Parallel()

	manager := New(Options{
		Servers:         []Server{fixtureServer(t, "fixture")},
		BaseEnvironment: os.Environ(),
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })

	manager.Refresh(t.Context())
	manager.Refresh(t.Context())

	assert.Contains(t, toolNames(manager.Tools()), "mcp__fixture__echo")

	var missing *Manager

	missing.Refresh(t.Context())
}

func callToolOf(
	t *testing.T,
	tools []elelem.Tool,
	name string,
) (elelem.ToolResult, error) {
	t.Helper()

	for _, tool := range tools {
		if tool.Name == name {
			return tool.Handler(t.Context(), elelem.ToolInput{
				Name: name, Arguments: json.RawMessage(`{}`),
			})
		}
	}

	require.FailNow(t, "tool not found", name)

	return elelem.ToolResult{}, nil
}
