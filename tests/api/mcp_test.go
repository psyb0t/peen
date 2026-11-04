//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"

	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mcpTestStatusPath = apiBasePath + "/session/mcp"
	mcpTestToolName   = "mcp__fixture__echo"
	mcpTestTask       = "echo through the MCP server"

	// mcpTestServerScript is a stdio MCP server with one echo tool, written
	// in bash and jq because the worker image has no other interpreter. It
	// answers one JSON-RPC request per line and ignores notifications.
	mcpTestServerScript = `while IFS= read -r line; do
  id=$(jq -c '.id // empty' <<<"$line")
  [ -z "$id" ] && continue
  case "$(jq -r '.method' <<<"$line")" in
    initialize)
      result=$(jq -c '{protocolVersion: .params.protocolVersion,
        capabilities: {tools: {}},
        serverInfo: {name: "fixture", version: "1.0.0"}}' <<<"$line") ;;
    tools/list)
      result='{"tools":[{"name":"echo","description":"Echoes the message.","inputSchema":{"type":"object","properties":{"message":{"type":"string"}}}}]}' ;;
    tools/call)
      result=$(jq -c '{content: [{type: "text",
        text: ("mcp echo: " + (.params.arguments.message // ""))}]}' <<<"$line") ;;
    *)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown method"}}\n' "$id"
      continue ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`

	mcpTestConfig = `{"mcpServers": {
  "fixture": {"command": "bash", "args": ["mcp-server.sh"]},
  "broken": {"command": "/does/not/exist"},
  "bad_name": {"command": "bash"}
}}`
)

func mcpStatus(t *testing.T, workspace freshWorkspace) api.MCPStatusReport {
	t.Helper()

	headers := withHeader(authenticatedHeaders(), headerSessionID,
		workspace.sessionID.String())
	response := apiRequest(t, http.MethodGet, mcpTestStatusPath, nil, headers)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.MCPStatusReport](t, response)
}

// A workspace's MCP server starts with the session's worker, its tool runs
// through a turn, and the status endpoint reports it next to the servers
// that failed and why.
func TestMCPServerToolRunsAndItsStatusIsReported(t *testing.T) {
	logAppOnImageFailure(t)

	workspace := openFreshWorkspace(t, "mcp-", map[string]string{
		".agents/mcp.json": mcpTestConfig,
		"mcp-server.sh":    mcpTestServerScript,
	})

	before := mcpStatus(t, workspace)
	assert.Empty(t, before.Servers, "no turn has reported yet")
	assert.Nil(t, before.ReportedAt)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: mcpTestTask, FinalAnswer: "echoed",
		Calls: []testinfra.ScriptedToolCall{{
			Name:      mcpTestToolName,
			Arguments: map[string]any{"message": "hi"},
		}},
	})

	connection := openSessionConnection(t, workspace.sessionID)
	requestsBefore := len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, mcpTestTask)
	frames := readFramesUntilTurnCompleted(t, connection)

	require.Len(t, framesOfType(frames, "mcp.status"), 1)

	requests := providerRequestsSince(requestsBefore)
	require.GreaterOrEqual(t, len(requests), 2)

	echoed := false
	for _, message := range requests[len(requests)-1] {
		if message.Role == "tool" &&
			strings.Contains(message.Content, "mcp echo: hi") {
			echoed = true
		}
	}

	assert.True(t, echoed, "the model sees the MCP tool's result")

	report := mcpStatus(t, workspace)
	require.NotNil(t, report.ReportedAt)

	byName := map[string]api.MCPServerStatus{}
	for _, server := range report.Servers {
		byName[server.Name] = server
	}

	require.Contains(t, byName, "fixture")
	assert.Equal(t, api.MCPServerStatusStateReady, byName["fixture"].State)
	assert.Equal(t, []string{mcpTestToolName}, byName["fixture"].Tools)
	assert.Equal(t, api.MCPServerStatusSourceWorkspace,
		byName["fixture"].Source)

	require.Contains(t, byName, "broken")
	assert.Equal(t, api.MCPServerStatusStateFailed, byName["broken"].State)
	require.NotNil(t, byName["broken"].Error)
	assert.NotContains(t, *byName["broken"].Error, ".go:",
		"the error carries no source location")

	require.Contains(t, byName, "bad_name")
	assert.Equal(t, api.MCPServerStatusStateFailed, byName["bad_name"].State)
}
