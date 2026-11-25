package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPMountPolicyAndAuthentication(t *testing.T) {
	t.Parallel()

	policy, err := (config.Config{}).MCPServerPolicy()
	require.NoError(t, err)
	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(uuid.New()), APIToken: testAPIToken,
		MCPPolicy: policy,
		MCPServer: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil),
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		token    bool
		host     string
		origins  []string
		fetch    string
		status   int
		envelope *aichteeteapee.ErrorResponse
	}{
		{name: "unauthenticated", host: "attacker.example", status: http.StatusUnauthorized},
		{name: "unknown host", token: true, host: "attacker.example", status: http.StatusMisdirectedRequest, envelope: &aichteeteapee.ErrorResponseMisdirectedRequest},
		{name: "unknown origin", token: true, host: "localhost:8080", origins: []string{"https://attacker.example"}, status: http.StatusForbidden, envelope: &aichteeteapee.ErrorResponseForbidden},
		{name: "duplicate origin", token: true, host: "localhost", origins: []string{"http://localhost:8080", "http://localhost:8080"}, status: http.StatusForbidden, envelope: &aichteeteapee.ErrorResponseForbidden},
		{name: "cross-site post without origin", token: true, host: "localhost", fetch: "cross-site", status: http.StatusForbidden, envelope: &aichteeteapee.ErrorResponseForbidden},
		{name: "allowed origin", token: true, host: "localhost", origins: []string{"http://localhost:8080"}, status: http.StatusMethodNotAllowed},
		{name: "no origin", token: true, host: "localhost", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Cross-origin protection passes safe methods, so only the
			// Sec-Fetch-Site case needs an unsafe one.
			method := http.MethodOptions
			if tc.fetch != "" {
				method = http.MethodPost
			}
			request := httptest.NewRequestWithContext(t.Context(), method, mcpBaseURL, nil)
			request.Host = tc.host
			request.Header["Origin"] = tc.origins
			if tc.fetch != "" {
				request.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			if tc.token {
				request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)
			}
			response := httptest.NewRecorder()
			instance.inProcessHandler.ServeHTTP(response, request)
			assert.Equal(t, tc.status, response.Code, response.Body.String())
			assert.NotContains(t, response.Body.String(), "attacker.example")
			if tc.envelope == nil {
				return
			}
			body := aichteeteapee.ErrorResponse{}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, *tc.envelope, body)
		})
	}
}

// A tool call that runs past the listener's write deadline still answers,
// because the MCP route clears that deadline for its own requests.
func TestMCPToolCallOutlivesListenerWriteDeadline(t *testing.T) {
	t.Parallel()

	const writeTimeout = 50 * time.Millisecond
	protocol := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(protocol, &mcp.Tool{Name: "slow", Description: "Answer after the write deadline."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			select {
			case <-time.After(5 * writeTimeout):
			case <-ctx.Done():
				return nil, struct{}{}, ctx.Err()
			}

			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, struct{}{}, nil
		})
	policy, err := (config.Config{}).MCPServerPolicy()
	require.NoError(t, err)
	instance := &Server{deps: Dependencies{MCPServer: protocol, MCPPolicy: policy}}
	require.NoError(t, instance.configureMCPHandler())
	httpServer := httptest.NewUnstartedServer(instance.mcpHTTPHandler)
	httpServer.Config.WriteTimeout = writeTimeout
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "done", text.Text)
}

func TestDisabledMCPDoesNotReachSPA(t *testing.T) {
	t.Parallel()

	instance, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)
	for _, path := range []string{mcpBaseURL, mcpBaseURL + "/anything"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		instance.inProcessHandler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code)
	}
}

func TestMCPHTTPDisconnectCancelsModernAndLegacyCalls(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"2025-03-26", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			started := make(chan struct{})
			cancelled := make(chan struct{})
			release := make(chan struct{})
			protocol := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			mcp.AddTool(protocol, &mcp.Tool{Name: "wait", Description: "Wait for cancellation."},
				func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
					close(started)
					select {
					case <-ctx.Done():
						close(cancelled)
					case <-release:
					}

					return &mcp.CallToolResult{IsError: true}, struct{}{}, nil
				})
			policy, err := (config.Config{}).MCPServerPolicy()
			require.NoError(t, err)
			instance := &Server{deps: Dependencies{MCPServer: protocol, MCPPolicy: policy}}
			require.NoError(t, instance.configureMCPHandler())
			httpServer := httptest.NewServer(instance.mcpHTTPHandler)
			defer httpServer.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
				Endpoint: httpServer.URL, DisableStandaloneSSE: true,
			}, &mcp.ClientSessionOptions{ProtocolVersion: version})
			require.NoError(t, err)
			defer func() { require.NoError(t, session.Close()) }()
			defer close(release)
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, callErr := session.CallTool(caller, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
				finished <- callErr
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("tool did not start")
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP cancellation did not reach tool")
			}
			require.Error(t, <-finished)
		})
	}
}

func TestMCPBodyLimitRejectsOversize(t *testing.T) {
	t.Parallel()

	protocol := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	policy, err := (config.Config{}).MCPServerPolicy()
	require.NoError(t, err)
	instance := &Server{deps: Dependencies{MCPServer: protocol, MCPPolicy: policy, MaxFileAPIBytes: 1}}
	require.NoError(t, instance.configureMCPHandler())
	body := strings.NewReader(strings.Repeat("x", int(fileRequestBodyOverhead)+100))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, mcpBaseURL, body)
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	instance.mcpHTTPHandler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
}
