package mcptools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSecretVariable = "TEST_MCP_TOKEN" //nolint:gosec // A variable name.
	testWorkspaceName  = "workspace"
	testConfigName     = "config"
)

func writeMCPConfig(t *testing.T, layer, content string) {
	t.Helper()

	directory := filepath.Join(layer, agentsDirectoryName)
	require.NoError(t, os.MkdirAll(directory, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(directory, FileName), []byte(content), 0o600,
	))
}

func testLayers(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	config := filepath.Join(root, testConfigName)
	workspace := filepath.Join(root, testWorkspaceName)

	require.NoError(t, os.MkdirAll(config, 0o750))
	require.NoError(t, os.MkdirAll(workspace, 0o750))

	return config, workspace
}

func failureFor(t *testing.T, failures []Status, name string) Status {
	t.Helper()

	for _, failure := range failures {
		if failure.Name == name {
			return failure
		}
	}

	require.FailNow(t, "no failure for server", name)

	return Status{}
}

func TestLoadReadsValidServers(t *testing.T) {
	t.Parallel()

	config, workspace := testLayers(t)
	writeMCPConfig(t, config, `{"mcpServers": {
		"github": {
			"command": "github-mcp-server", "args": ["stdio"],
			"env": {"LOG": "1"}, "envFrom": {"GITHUB_TOKEN": "TEST_MCP_TOKEN"},
			"cwd": "tools", "tools": ["create_issue"]
		},
		"docs": {
			"type": "http", "url": "https://docs.example.com/mcp",
			"headers": {"X-Team": "core"}, "bearerTokenEnv": "TEST_MCP_TOKEN"
		}
	}}`)

	servers, failures := Load(LoadOptions{
		ConfigDirectory: config, Workspace: workspace,
		SecretEnv: []string{testSecretVariable},
	})
	require.Empty(t, failures)
	require.Len(t, servers, 2)

	docs, github := servers[0], servers[1]

	assert.Equal(t, Server{
		Name: "docs", Source: SourceConfig, Transport: TransportHTTP,
		URL:            "https://docs.example.com/mcp",
		Headers:        map[string]string{"X-Team": "core"},
		BearerTokenEnv: testSecretVariable,
	}, docs)

	assert.Equal(t, Server{
		Name: "github", Source: SourceConfig, Transport: TransportStdio,
		Command: "github-mcp-server", Args: []string{"stdio"},
		Env:       map[string]string{"LOG": "1"},
		EnvFrom:   map[string]string{"GITHUB_TOKEN": testSecretVariable},
		Directory: filepath.Join(workspace, "tools"),
		Tools:     []string{"create_issue"},
	}, github)
}

func TestLoadWithoutAnyFile(t *testing.T) {
	t.Parallel()

	config, workspace := testLayers(t)

	servers, failures := Load(LoadOptions{
		ConfigDirectory: config, Workspace: workspace, WorkspaceEnabled: true,
	})
	assert.Empty(t, servers)
	assert.Empty(t, failures)
}

func TestLoadRefusesBadEntries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		server  string
		wantErr string
	}{
		{
			name: "snake_case", server: `{"command": "x"}`,
			wantErr: "camelCase",
		},
		{
			name: "noCommand", server: `{"type": "stdio"}`,
			wantErr: "needs a command",
		},
		{
			name: "legacySse", server: `{"type": "sse", "url": "https://x"}`,
			wantErr: `type "sse" is not supported`,
		},
		{
			name: "badUrl", server: `{"type": "http", "url": "ftp://x"}`,
			wantErr: "http or https url",
		},
		{
			name:    "mixedHttp",
			server:  `{"url": "https://x", "command": "y"}`,
			wantErr: "are for stdio servers",
		},
		{
			name:    "mixedStdio",
			server:  `{"command": "y", "headers": {"A": "b"}}`,
			wantErr: "are for http servers",
		},
		{
			name: "inlineToken",
			server: `{"url": "https://x",
				"headers": {"authorization": "Bearer s3cr3t"}}`,
			wantErr: "bearerTokenEnv",
		},
		{
			name:    "unlistedSecret",
			server:  `{"command": "y", "envFrom": {"A": "OTHER"}}`,
			wantErr: `"OTHER" is not listed in PEEN_MCP_ENV`,
		},
		{
			name:    "badEnvName",
			server:  `{"command": "y", "env": {"A=B": "c"}}`,
			wantErr: "invalid variable name",
		},
		{
			name:    "unknownKey",
			server:  `{"command": "y", "disabled": true}`,
			wantErr: "unknown field",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config, workspace := testLayers(t)
			writeMCPConfig(t, config,
				`{"mcpServers": {"`+tc.name+`": `+tc.server+`,
				"good": {"command": "y"}}}`)

			servers, failures := Load(LoadOptions{
				ConfigDirectory: config, Workspace: workspace,
				SecretEnv: []string{testSecretVariable},
			})

			require.Len(t, servers, 1, "a bad entry does not stop the others")
			assert.Equal(t, "good", servers[0].Name)

			failure := failureFor(t, failures, tc.name)
			assert.Equal(t, StateFailed, failure.State)
			assert.Contains(t, failure.Error, tc.wantErr)
			assert.NotContains(t, failure.Error, "s3cr3t")
			assert.NotContains(t, failure.Error, ".go:")
		})
	}
}

func TestLoadRefusesAWholeBadFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content string
	}{
		{name: "not JSON", content: `{"mcpServers": `},
		{name: "unknown top-level key", content: `{"servers": {}}`},
		{name: "trailing data", content: `{"mcpServers": {}} {}`},
		{
			name:    "too large",
			content: `{"mcpServers": {}}` + strings.Repeat(" ", maxConfigBytes),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config, workspace := testLayers(t)
			writeMCPConfig(t, config, tc.content)

			servers, failures := Load(LoadOptions{
				ConfigDirectory: config, Workspace: workspace,
			})
			assert.Empty(t, servers)
			require.Len(t, failures, 1)
			assert.Empty(t, failures[0].Name)
			assert.Equal(t, SourceConfig, failures[0].Source)
			assert.Equal(t, StateFailed, failures[0].State)
		})
	}
}

func TestLoadWorkspaceLayer(t *testing.T) {
	t.Parallel()

	const workspaceConfig = `{"mcpServers": {
		"local": {"command": "local-server"},
		"shared": {"command": "workspace-copy"},
		"leaky": {"command": "x", "envFrom": {"A": "TEST_MCP_TOKEN"}}
	}}`

	t.Run("ignored unless enabled", func(t *testing.T) {
		t.Parallel()

		config, workspace := testLayers(t)
		writeMCPConfig(t, workspace, workspaceConfig)

		servers, failures := Load(LoadOptions{
			ConfigDirectory: config, Workspace: workspace,
			SecretEnv: []string{testSecretVariable},
		})
		assert.Empty(t, servers)
		assert.Empty(t, failures)
	})

	t.Run("merged when enabled", func(t *testing.T) {
		t.Parallel()

		config, workspace := testLayers(t)
		writeMCPConfig(t, config,
			`{"mcpServers": {"shared": {"command": "config-copy"}}}`)
		writeMCPConfig(t, workspace, workspaceConfig)

		servers, failures := Load(LoadOptions{
			ConfigDirectory: config, Workspace: workspace,
			WorkspaceEnabled: true,
			SecretEnv:        []string{testSecretVariable},
		})

		require.Len(t, servers, 2)
		assert.Equal(t, "config-copy", servers[0].Command,
			"the config directory's server wins")
		assert.Equal(t, "local", servers[1].Name)
		assert.Equal(t, SourceWorkspace, servers[1].Source)
		assert.Equal(t, workspace, servers[1].Directory)

		assert.Contains(t, failureFor(t, failures, "shared").Error,
			"already defines")
		assert.Contains(t, failureFor(t, failures, "leaky").Error,
			"only allowed in the config directory",
			"a file the agent can write gets no secrets")
	})
}

func TestLoadCapsTheServerCount(t *testing.T) {
	t.Parallel()

	config, workspace := testLayers(t)

	entries := make([]string, 0, maxServers+2)
	for index := range maxServers + 2 {
		entries = append(entries,
			`"s`+strconv.Itoa(index)+`": {"command": "x"}`)
	}

	writeMCPConfig(t, config,
		`{"mcpServers": {`+strings.Join(entries, ",")+`}}`)

	servers, failures := Load(LoadOptions{
		ConfigDirectory: config, Workspace: workspace,
	})
	assert.Len(t, servers, maxServers)
	assert.Len(t, failures, 2)
}
