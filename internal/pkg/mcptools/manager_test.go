package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testServerFile    = "server.sh"
	testSecretValue   = "test-secret-value"
	testStartTimeout  = 30 * time.Second
	testMissingSecret = "TEST_MCP_MISSING"

	// testServerScript is a stdio MCP server in bash and jq. It offers echo,
	// and secret, which returns the SECRET variable the server was started
	// with, so a test can see envFrom reach it.
	testServerScript = `while IFS= read -r line; do
  id=$(jq -c '.id // empty' <<<"$line")
  [ -z "$id" ] && continue
  case "$(jq -r '.method' <<<"$line")" in
    initialize)
      result=$(jq -c '{protocolVersion: .params.protocolVersion,
        capabilities: {tools: {}},
        serverInfo: {name: "fixture", version: "1.0.0"}}' <<<"$line") ;;
    tools/list)
      result='{"tools":[{"name":"echo","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}},{"name":"exit","inputSchema":{"type":"object"}}]}' ;;
    tools/call)
      [ "$(jq -r '.params.name' <<<"$line")" = exit ] && exit 1
      if [ "$(jq -r '.params.name' <<<"$line")" = secret ]; then
        result=$(jq -nc --arg text "$SECRET" '{content: [{type: "text", text: $text}]}')
      else
        result=$(jq -c '{content: [{type: "text",
          text: ("mcp echo: " + (.params.arguments.message // ""))}]}' <<<"$line")
      fi ;;
    *)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown"}}\n' "$id"
      continue ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
)

func testServerPath(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), testServerFile)
	require.NoError(t, os.WriteFile(path, []byte(testServerScript), 0o600))

	return path
}

func lookupTestSecret(name string) (string, bool) {
	if name == testSecretVariable {
		return testSecretValue, true
	}

	return "", false
}

func statusByName(statuses []Status) map[string]Status {
	byName := make(map[string]Status, len(statuses))
	for _, status := range statuses {
		byName[status.Name] = status
	}

	return byName
}

func callTestTool(
	t *testing.T,
	tools []elelem.Tool,
	name, arguments string,
) elelem.ToolResult {
	t.Helper()

	for _, tool := range tools {
		if tool.Name != name {
			continue
		}

		result, err := tool.Handler(t.Context(), elelem.ToolInput{
			Name: name, Arguments: json.RawMessage(arguments),
		})
		require.NoError(t, err)

		return result
	}

	require.FailNow(t, "tool not found", name)

	return elelem.ToolResult{}
}

func TestManagerStartsServersAndReportsStatus(t *testing.T) {
	t.Parallel()

	refused := failedStatus("", SourceWorkspace, "", ErrInvalidConfig)

	manager := New(Options{
		Servers: []Server{
			{
				Name: "fixture", Source: SourceConfig,
				Transport: TransportStdio, Command: "bash",
				Args:    []string{testServerPath(t)},
				EnvFrom: map[string]string{"SECRET": testSecretVariable},
			},
			{
				Name: "broken", Source: SourceConfig,
				Transport: TransportStdio, Command: "/does/not/exist",
			},
			{
				Name: "remote", Source: SourceConfig,
				Transport: TransportHTTP, URL: "https://127.0.0.1:1/mcp",
				BearerTokenEnv: testMissingSecret,
			},
		},
		Failures:        []Status{refused},
		BaseEnvironment: os.Environ(),
		LookupEnv:       lookupTestSecret,
		StartTimeout:    testStartTimeout,
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })

	require.True(t, manager.Configured())
	assert.Nil(t, manager.Tools(), "nothing runs before Start")

	manager.Start(t.Context())

	statuses := manager.Status(t.Context())
	require.Len(t, statuses, 4)
	assert.Equal(t, refused, statuses[3], "refused entries come last")

	byName := statusByName(statuses)

	assert.Equal(t, Status{
		Name:      "fixture",
		Source:    SourceConfig,
		Transport: TransportStdio,
		State:     StateReady,
		Tools: []string{
			"mcp__fixture__echo", "mcp__fixture__secret", "mcp__fixture__exit",
		},
		Warnings: []string{},
	}, byName["fixture"])

	assert.Equal(t, StateFailed, byName["broken"].State)
	assert.NotEmpty(t, byName["broken"].Error)

	assert.Equal(t, StateFailed, byName["remote"].State)
	assert.Equal(t,
		`variable "TEST_MCP_MISSING" is empty: invalid MCP configuration`,
		byName["remote"].Error)

	tools := manager.Tools()
	require.Len(t, tools, 3)

	assert.Equal(t, "mcp echo: hi",
		callTestTool(t, tools, "mcp__fixture__echo", `{"message":"hi"}`).Content)
	assert.Equal(t, testSecretValue,
		callTestTool(t, tools, "mcp__fixture__secret", `{}`).Content,
		"envFrom reaches the server")

	for _, status := range statuses {
		assert.NotContains(t, status.Error, testSecretValue)
	}

	require.NoError(t, manager.Close())
	require.NoError(t, manager.Close(), "Close is idempotent")
}

func TestManagerWithoutServers(t *testing.T) {
	t.Parallel()

	var missing *Manager

	assert.False(t, missing.Configured())
	assert.Nil(t, missing.Tools())
	assert.Nil(t, missing.Status(t.Context()))
	require.NoError(t, missing.Close())
	missing.Start(t.Context())

	empty := New(Options{})
	assert.False(t, empty.Configured())
	empty.Start(t.Context())
	assert.Empty(t, empty.Tools())
	assert.Empty(t, empty.Status(t.Context()))
}

func TestManagerClosedBeforeStartStartsNothing(t *testing.T) {
	t.Parallel()

	manager := New(Options{Servers: []Server{{
		Name: "fixture", Source: SourceConfig, Transport: TransportStdio,
		Command: "bash", Args: []string{testServerPath(t)},
	}}})

	require.NoError(t, manager.Close())

	ctx, cancel := context.WithTimeout(t.Context(), testStartTimeout)
	defer cancel()

	manager.Start(ctx)
	assert.Empty(t, manager.Tools())
}
