package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerCapabilities(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	initialized := client.InitializeResult()
	require.NotNil(t, initialized)
	encoded, err := json.Marshal(initialized.Capabilities)
	require.NoError(t, err)
	// No logging, prompts or subscriptions, and no list ever changes.
	assert.JSONEq(t, `{"tools":{},"resources":{},"completions":{}}`,
		string(encoded))
}

func TestServerInstructions(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	instructions := client.InitializeResult().Instructions
	require.NotEmpty(t, instructions)
	assert.NotContains(t, instructions, "\n")
	for _, name := range []string{
		toolSendMessage, toolListSessions, toolCancelTurn, toolListModels,
		uriSessions, templateSession, templateMessages,
	} {
		assert.Contains(t, instructions, name)
	}
}

func TestResultsArePrivateAndUncached(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	listed, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	resources, err := client.ListResources(t.Context(), nil)
	require.NoError(t, err)
	templates, err := client.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	read, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: sessionURI(uuid.New()),
	})
	require.NoError(t, err)
	for name, cache := range map[string]mcp.Cacheable{
		"tools/list":               listed.Cacheable,
		"resources/list":           resources.Cacheable,
		"resources/templates/list": templates.Cacheable,
		"resources/read":           read.Cacheable,
	} {
		assert.Equal(t, cacheScopePrivate, cache.CacheScope, name)
		assert.Zero(t, cache.TTLMs, name)
	}
}

func TestNewValidatesDependencies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Dependencies)
	}{
		{"missing runner", func(d *Dependencies) { d.Runner = nil }},
		{"missing files", func(d *Dependencies) { d.Files = nil }},
		{"negative schema", func(d *Dependencies) {
			d.MaxOutputSchemaBytes = -1
		}},
		{"negative messages", func(d *Dependencies) {
			d.MaxResourceMessages = -1
		}},
		{"too many messages", func(d *Dependencies) {
			d.MaxResourceMessages = 201
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := (&testBoundary{}).dependencies()
			tc.change(&deps)
			_, err := New(deps)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
