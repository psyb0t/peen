package mcptools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/psyb0t/elelem/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	featureServerFile = "features.sh"
	featureServerName = "notes"

	featureInstructions = "Read notes with readMcpResource."

	// featureServerScript is a stdio MCP server in bash and jq that offers
	// instructions, a resource, a template, a prompt, and an ask tool that
	// asks the client a question through elicitation and returns the answer.
	featureServerScript = `while IFS= read -r line; do
  id=$(jq -c '.id // empty' <<<"$line")
  [ -z "$id" ] && continue
  case "$(jq -r '.method' <<<"$line")" in
    initialize)
      result=$(jq -c '{protocolVersion: .params.protocolVersion,
        capabilities: {tools: {}, resources: {}, prompts: {}},
        serverInfo: {name: "features", title: "Feature fixture",
          version: "2.0.0"},
        instructions: "Read notes with readMcpResource."}' <<<"$line") ;;
    tools/list)
      result='{"tools":[{"name":"ask","inputSchema":{"type":"object"}}]}' ;;
    tools/call)
      printf '%s\n' '{"jsonrpc":"2.0","id":"ask-1","method":"elicitation/create","params":{"message":"Who are you?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}'
      IFS= read -r reply
      result=$(jq -c '{content: [{type: "text",
        text: (.result | tojson)}]}' <<<"$reply") ;;
    resources/list)
      result='{"resources":[{"uri":"note://one","name":"one","mimeType":"text/plain"}]}' ;;
    resources/templates/list)
      result='{"resourceTemplates":[{"uriTemplate":"note://{name}","name":"note"}]}' ;;
    resources/read)
      result=$(jq -c '{contents: [{uri: .params.uri, mimeType: "text/plain",
        text: ("contents of " + .params.uri)}]}' <<<"$line") ;;
    prompts/list)
      result='{"prompts":[{"name":"review","description":"Review a change","arguments":[{"name":"focus","description":"What to look at","required":true}]}]}' ;;
    prompts/get)
      result=$(jq -c '{messages: [{role: "user", content: {type: "text",
        text: ("Review with focus " + .params.arguments.focus)}}]}' <<<"$line") ;;
    *)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown"}}\n' "$id"
      continue ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
)

func featureServer(t *testing.T) Server {
	t.Helper()

	path := filepath.Join(t.TempDir(), featureServerFile)
	require.NoError(t, os.WriteFile(path, []byte(featureServerScript), 0o600))

	return Server{
		Name: featureServerName, Source: SourceConfig,
		Transport: TransportStdio, Command: "bash", Args: []string{path},
		Instructions: true, Resources: true, Prompts: true,
	}
}

func featureManager(
	t *testing.T,
	maxInstructionsBytes int,
	servers ...Server,
) *Manager {
	t.Helper()

	manager := New(Options{
		Servers:              servers,
		BaseEnvironment:      os.Environ(),
		StartTimeout:         testStartTimeout,
		MaxInstructionsBytes: maxInstructionsBytes,
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })

	manager.Start(t.Context())

	return manager
}

func TestManagerReportsWhatAServerOffers(t *testing.T) {
	t.Parallel()

	manager := featureManager(t, 4096, featureServer(t))

	status := statusByName(manager.Status(t.Context()))[featureServerName]
	require.Equal(t, StateReady, status.State, status.Error)

	assert.Equal(t, "2025-11-25", status.ProtocolVersion)
	assert.Equal(t, &ServerInfo{
		Name: "features", Title: "Feature fixture", Version: "2.0.0",
	}, status.ServerInfo)
	assert.Equal(t, []Feature{
		FeatureTools, FeatureResources, FeaturePrompts,
	}, status.Features)
	assert.Equal(t, len(featureInstructions), status.InstructionsBytes)
	assert.True(t, status.InstructionsIncluded)
	assert.False(t, status.InstructionsCut)
	assert.Equal(t, []PromptInfo{{
		Name: "review", Description: "Review a change",
		Arguments: []PromptArgumentInfo{{
			Name: "focus", Description: "What to look at", Required: true,
		}},
	}}, status.Prompts)
}

func TestManagerInstructions(t *testing.T) {
	t.Parallel()

	t.Run("in configuration order with the limit", func(t *testing.T) {
		t.Parallel()

		manager := featureManager(t, 4096, featureServer(t))

		assert.Equal(t, []ServerInstructions{{
			Server: featureServerName, Source: SourceConfig,
			Text: featureInstructions, Limit: 4096,
		}}, manager.Instructions())
	})

	t.Run("cut to the limit", func(t *testing.T) {
		t.Parallel()

		manager := featureManager(t, 9, featureServer(t))

		instructions := manager.Instructions()
		require.Len(t, instructions, 1)
		assert.Equal(t, featureInstructions[:9], instructions[0].Text)
		assert.True(t, instructions[0].Cut)

		status := statusByName(manager.Status(t.Context()))[featureServerName]
		assert.True(t, status.InstructionsCut)
	})

	t.Run("zero leaves them out", func(t *testing.T) {
		t.Parallel()

		manager := featureManager(t, 0, featureServer(t))

		assert.Empty(t, manager.Instructions())

		status := statusByName(manager.Status(t.Context()))[featureServerName]
		assert.False(t, status.InstructionsIncluded)
	})

	t.Run("an entry can turn them off", func(t *testing.T) {
		t.Parallel()

		server := featureServer(t)
		server.Instructions = false
		manager := featureManager(t, 4096, server)

		assert.Empty(t, manager.Instructions())

		status := statusByName(manager.Status(t.Context()))[featureServerName]
		assert.False(t, status.InstructionsIncluded)
	})
}

func TestManagerResourcesAndPrompts(t *testing.T) {
	t.Parallel()

	manager := featureManager(t, 4096, featureServer(t))
	ctx := t.Context()

	assert.Equal(t, []string{featureServerName}, manager.ResourceServers())
	assert.Equal(t, []string{featureServerName}, manager.PromptServers())

	resources, err := manager.ListResources(ctx, featureServerName, "")
	require.NoError(t, err)
	assert.Equal(t, []mcp.Resource{{
		URI: "note://one", Name: "one", MIMEType: "text/plain",
	}}, resources.Resources)

	templates, err := manager.ListResourceTemplates(ctx, featureServerName,
		"")
	require.NoError(t, err)
	assert.Equal(t, []mcp.ResourceTemplate{{
		URITemplate: "note://{name}", Name: "note",
	}}, templates.Templates)

	contents, err := manager.ReadResource(ctx, featureServerName,
		"note://two")
	require.NoError(t, err)
	require.Len(t, contents, 1)
	assert.Equal(t, "contents of note://two", contents[0].Text)

	prompts, err := manager.ListPrompts(ctx, featureServerName, "")
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 1)
	assert.Equal(t, "review", prompts.Prompts[0].Name)

	prompt, err := manager.GetPrompt(ctx, featureServerName, "review",
		map[string]string{"focus": "errors"})
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	assert.Equal(t, mcp.RoleUser, prompt.Messages[0].Role)
	assert.Equal(t, "Review with focus errors",
		prompt.Messages[0].Content.String())

	_, err = manager.ListResources(ctx, "missing", "")
	require.ErrorIs(t, err, ErrServerUnavailable)
}

func TestManagerHonorsTurnedOffFeatures(t *testing.T) {
	t.Parallel()

	server := featureServer(t)
	server.Resources = false
	server.Prompts = false
	manager := featureManager(t, 4096, server)
	ctx := t.Context()

	assert.Empty(t, manager.ResourceServers())
	assert.Empty(t, manager.PromptServers())

	_, err := manager.ReadResource(ctx, featureServerName, "note://one")
	require.ErrorIs(t, err, mcp.ErrNotSupported)

	_, err = manager.GetPrompt(ctx, featureServerName, "review", nil)
	require.ErrorIs(t, err, mcp.ErrNotSupported)

	status := statusByName(manager.Status(ctx))[featureServerName]
	assert.Empty(t, status.Prompts, "turned-off prompts are not listed")
}

// recordingElicitor answers every question with one name and keeps what it
// was asked.
type recordingElicitor struct {
	mutex    sync.Mutex
	servers  []string
	messages []string
}

func (r *recordingElicitor) elicit(
	_ context.Context,
	server string,
	request mcp.ElicitRequest,
) (mcp.ElicitResult, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.servers = append(r.servers, server)
	r.messages = append(r.messages, request.Message)

	return mcp.ElicitResult{
		Action:  mcp.ElicitActionAccept,
		Content: map[string]any{"name": "Ada"},
	}, nil
}

func TestManagerRoutesElicitation(t *testing.T) {
	t.Parallel()

	t.Run("to the elicitor", func(t *testing.T) {
		t.Parallel()

		manager := featureManager(t, 4096, featureServer(t))
		elicitor := &recordingElicitor{}
		manager.SetElicitor(elicitor.elicit)

		result := callTestTool(t, manager.Tools(), "mcp__notes__ask", `{}`)
		assert.JSONEq(t, `{"action":"accept","content":{"name":"Ada"}}`,
			result.Content)

		elicitor.mutex.Lock()
		defer elicitor.mutex.Unlock()

		assert.Equal(t, []string{featureServerName}, elicitor.servers)
		assert.Equal(t, []string{"Who are you?"}, elicitor.messages)
	})

	t.Run("declined without one", func(t *testing.T) {
		t.Parallel()

		manager := featureManager(t, 4096, featureServer(t))

		result := callTestTool(t, manager.Tools(), "mcp__notes__ask", `{}`)
		assert.JSONEq(t, `{"action":"decline"}`, result.Content)
	})
}
