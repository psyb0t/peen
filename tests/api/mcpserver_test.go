//go:build integration

package api_test

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/aichteeteapee"
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
		auth     bool
		host     string
		origin   string
		status   int
		envelope *aichteeteapee.ErrorResponse
	}{
		{status: http.StatusUnauthorized},
		{host: "attacker.example", status: http.StatusUnauthorized},
		{auth: true, host: "attacker.example", status: http.StatusMisdirectedRequest, envelope: &aichteeteapee.ErrorResponseMisdirectedRequest},
		{auth: true, host: "localhost", origin: "https://attacker.example", status: http.StatusForbidden, envelope: &aichteeteapee.ErrorResponseForbidden},
		// The SDK answers 405 to GET in stateless mode, which proves the
		// request passed the Host and Origin checks.
		{auth: true, host: "localhost", origin: "http://localhost:8080", status: http.StatusMethodNotAllowed},
		{auth: true, host: "localhost", status: http.StatusMethodNotAllowed},
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
		assert.NotContains(t, string(body.Body), "attacker.example")
		if tc.envelope != nil {
			envelope := aichteeteapee.ErrorResponse{}
			require.NoError(t, json.Unmarshal(body.Body, &envelope))
			assert.Equal(t, *tc.envelope, envelope)
		}
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

const (
	mcpTestProgressWait = 30 * time.Second
	mcpTestFileContent  = "hello from an MCP resource"
)

// connectMCPServer connects the go-sdk client to a variant's /mcp/v1 with
// the test token. An empty protocol uses the SDK's latest version.
func connectMCPServer(
	t *testing.T,
	variant *testinfra.Infra,
	protocol string,
	options *mcp.ClientOptions,
) *mcp.ClientSession {
	t.Helper()
	httpClient := *variant.HTTPClient()
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = mcpAuthenticatedTransport{base: base}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, options)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: variant.APIURL("/mcp/v1"), HTTPClient: &httpClient,
		DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })

	return session
}

func readMCPResource(t *testing.T, session *mcp.ClientSession, uri string) *mcp.ResourceContents {
	t.Helper()
	result, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	require.NoError(t, err, uri)
	require.Len(t, result.Contents, 1)

	return result.Contents[0]
}

func readMCPResourceFailure(t *testing.T, session *mcp.ClientSession, uri string) *jsonrpc.Error {
	t.Helper()
	_, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	require.Error(t, err, uri)
	var failure *jsonrpc.Error
	require.ErrorAs(t, err, &failure)

	return failure
}

// callMCPTool calls a tool that must succeed.
func callMCPTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.Content)
}

func completeMCPArgument(t *testing.T, session *mcp.ClientSession, params *mcp.CompleteParams) []string {
	t.Helper()
	result, err := session.Complete(t.Context(), params)
	require.NoError(t, err)

	return result.Completion.Values
}

func TestAPIMCPServerResourcesCompletionAndProgress(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{Environment: map[string]string{
		"PEEN_MCP_SERVER": "true",
	}})
	for _, protocol := range []string{"", "2025-11-25"} {
		t.Run("protocol "+cmp.Or(protocol, "latest"), func(t *testing.T) {
			progress := make(chan *mcp.ProgressNotificationParams, 256)
			session := connectMCPServer(t, variant, protocol, &mcp.ClientOptions{
				ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) {
					select {
					case progress <- request.Params:
					default:
					}
				},
			})
			assert.NotEmpty(t, session.InitializeResult().Instructions)
			resources, err := session.ListResources(t.Context(), nil)
			require.NoError(t, err)
			require.Len(t, resources.Resources, 1)
			assert.Equal(t, "peen://sessions", resources.Resources[0].URI)
			templates, err := session.ListResourceTemplates(t.Context(), nil)
			require.NoError(t, err)
			uris := make([]string, 0, len(templates.ResourceTemplates))
			for _, template := range templates.ResourceTemplates {
				uris = append(uris, template.URITemplate)
			}
			assert.ElementsMatch(t, []string{
				"peen://session/{sessionId}",
				"peen://session/{sessionId}/messages",
				"peen://session/{sessionId}/files/{+path}",
			}, uris)

			token := "progress-" + cmp.Or(protocol, "latest")
			params := &mcp.CallToolParams{Name: "sendMessage", Arguments: map[string]any{
				"workspace": variantTestWorkspace, "message": "Explain briefly",
			}}
			params.SetProgressToken(token)
			sent, err := session.CallTool(t.Context(), params)
			require.NoError(t, err)
			require.False(t, sent.IsError, "%+v", sent.Content)
			select {
			case notification := <-progress:
				assert.Equal(t, token, notification.ProgressToken)
				assert.NotEmpty(t, notification.Message)
			case <-time.After(mcpTestProgressWait):
				t.Fatal("sendMessage sent no progress notification")
			}
			encoded, err := json.Marshal(sent.StructuredContent)
			require.NoError(t, err)
			var answer struct {
				SessionID string `json:"sessionId"`
			}
			require.NoError(t, json.Unmarshal(encoded, &answer))
			require.NotEmpty(t, answer.SessionID)
			sessionURI := "peen://session/" + answer.SessionID
			links := []string{}
			for _, content := range sent.Content {
				if link, ok := content.(*mcp.ResourceLink); ok {
					links = append(links, link.URI)
				}
			}
			assert.Equal(t, []string{sessionURI, sessionURI + "/messages"}, links)

			stored := readMCPResource(t, session, sessionURI)
			assert.Equal(t, "application/json", stored.MIMEType)
			var storedSession struct {
				ID        string `json:"id"`
				Workspace string `json:"workspace"`
			}
			require.NoError(t, json.Unmarshal([]byte(stored.Text), &storedSession))
			assert.Equal(t, answer.SessionID, storedSession.ID)
			assert.Equal(t, variantTestWorkspace, storedSession.Workspace)
			messages := readMCPResource(t, session, sessionURI+"/messages")
			var page struct {
				Items []struct {
					Role string `json:"role"`
				} `json:"items"`
			}
			require.NoError(t, json.Unmarshal([]byte(messages.Text), &page))
			assert.NotEmpty(t, page.Items)
			index := readMCPResource(t, session, "peen://sessions")
			assert.Contains(t, index.Text, `"uri":"`+sessionURI+`"`)

			directory := "mcp-resources-" + uuid.NewString()
			file := directory + "/note.txt"
			callMCPTool(t, session, "writeWorkspaceFile", map[string]any{
				"sessionId": answer.SessionID, "path": file, "createDirectories": true,
				"contentBase64": base64.StdEncoding.EncodeToString([]byte(mcpTestFileContent)),
			})
			defer func() {
				for _, path := range []string{file, directory} {
					callMCPTool(t, session, "deleteWorkspaceFile", map[string]any{
						"sessionId": answer.SessionID, "path": path,
					})
				}
			}()
			text := readMCPResource(t, session, sessionURI+"/files/"+file)
			assert.Equal(t, "text/plain", text.MIMEType)
			assert.Equal(t, mcpTestFileContent, text.Text)
			listing := readMCPResource(t, session, sessionURI+"/files/"+directory)
			assert.Equal(t, "application/json", listing.MIMEType)
			assert.Contains(t, listing.Text, `"name":"note.txt"`)

			for _, refused := range []string{"../etc/passwd", "%2e%2e/etc/passwd", "%2Fetc%2Fpasswd"} {
				failure := readMCPResourceFailure(t, session, sessionURI+"/files/"+refused)
				assert.Equal(t, "The session or workspace is not allowed.", failure.Message, refused)
				assert.NotContains(t, failure.Message, "passwd")
			}
			unknown := readMCPResourceFailure(t, session, "peen://session/"+uuid.NewString())
			assert.Equal(t, "Resource not found", unknown.Message)

			ids := completeMCPArgument(t, session, &mcp.CompleteParams{
				Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: "peen://session/{sessionId}"},
				Argument: mcp.CompleteParamsArgument{Name: "sessionId", Value: answer.SessionID[:8]},
			})
			assert.Contains(t, ids, answer.SessionID)
			paths := completeMCPArgument(t, session, &mcp.CompleteParams{
				Ref: &mcp.CompleteReference{
					Type: "ref/resource", URI: "peen://session/{sessionId}/files/{+path}",
				},
				Argument: mcp.CompleteParamsArgument{Name: "path", Value: directory + "/no"},
				Context: &mcp.CompleteContext{Arguments: map[string]string{
					"sessionId": answer.SessionID,
				}},
			})
			assert.Equal(t, []string{file}, paths)
		})
	}
}
