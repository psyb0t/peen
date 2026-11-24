//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mcpAuthenticatedTransport struct {
	base http.RoundTripper
}

func (tr mcpAuthenticatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	for name, value := range authenticatedHeaders() {
		request.Header.Set(name, value)
	}
	response, err := tr.base.RoundTrip(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "send MCP test request")
	}

	return response, nil
}

func TestAPIMCPServerRoundTripAndRefusals(t *testing.T) {
	disabled := apiRequest(t, http.MethodGet, "/mcp/v1", nil, authenticatedHeaders())
	requireAPIStatus(t, disabled, http.StatusNotFound)
	variant := startAPIVariant(t, testinfra.VariantOptions{Environment: map[string]string{
		"PEEN_MCP_SERVER": "true",
	}})
	for _, tc := range []struct {
		auth   bool
		host   string
		origin string
		status int
	}{
		{status: http.StatusUnauthorized},
		{auth: true, host: "attacker.example", status: http.StatusMisdirectedRequest},
		{auth: true, host: "localhost", origin: "https://attacker.example", status: http.StatusForbidden},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, variant.APIURL("/mcp/v1"), nil)
		require.NoError(t, err)
		if tc.auth {
			for name, value := range authenticatedHeaders() {
				request.Header.Set(name, value)
			}
		}
		if tc.host != "" {
			request.Host = tc.host
		}
		if tc.origin != "" {
			request.Header.Set("Origin", tc.origin)
		}
		response, err := variant.HTTPClient().Do(request)
		require.NoError(t, err)
		body := readAPIResponse(t, response)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, tc.status, body.StatusCode, string(body.Body))
	}
	httpClient := *variant.HTTPClient()
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = mcpAuthenticatedTransport{base: base}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: variant.APIURL("/mcp/v1"), HTTPClient: &httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()
	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	assert.Len(t, tools.Tools, 10)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sendMessage", Arguments: map[string]any{
			"workspace": variantTestWorkspace, "message": "Explain briefly",
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.Content)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var answer struct {
		SessionID string `json:"sessionId"`
		Text      string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(encoded, &answer))
	require.NotEmpty(t, answer.SessionID)
	assert.NotEmpty(t, answer.Text)
	conflict, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sendMessage", Arguments: map[string]any{
			"workspace": variantTestWorkspace, "message": "Explain briefly",
			"agentSpace": agentSpaceDockerDefault,
		},
	})
	require.NoError(t, err)
	require.True(t, conflict.IsError)
	require.Len(t, conflict.Content, 1)
	conflictText, ok := conflict.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, `this workspace runs in agent space "control", not "docker:default"`,
		conflictText.Text)
	for _, path := range []string{"../escape", "/etc/passwd"} {
		refused, callErr := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "readWorkspaceFile", Arguments: map[string]any{
				"sessionId": answer.SessionID, "path": path,
			},
		})
		require.NoError(t, callErr)
		assert.True(t, refused.IsError)
	}
}
