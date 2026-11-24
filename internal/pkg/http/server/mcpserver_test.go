package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		name    string
		token   bool
		host    string
		origins []string
		status  int
	}{
		{name: "unauthenticated", host: "attacker.example", status: http.StatusUnauthorized},
		{name: "unknown host", token: true, host: "attacker.example", status: http.StatusMisdirectedRequest},
		{name: "unknown origin", token: true, host: "localhost:8080", origins: []string{"https://attacker.example"}, status: http.StatusForbidden},
		{name: "duplicate origin", token: true, host: "localhost", origins: []string{"http://localhost:8080", "http://localhost:8080"}, status: http.StatusForbidden},
		{name: "allowed origin", token: true, host: "localhost", origins: []string{"http://localhost:8080"}, status: http.StatusMethodNotAllowed},
		{name: "no origin", token: true, host: "localhost", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, mcpBaseURL, nil)
			request.Host = tc.host
			request.Header["Origin"] = tc.origins
			if tc.token {
				request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)
			}
			response := httptest.NewRecorder()
			instance.inProcessHandler.ServeHTTP(response, request)
			assert.Equal(t, tc.status, response.Code, response.Body.String())
			assert.NotContains(t, response.Body.String(), "attacker.example")
		})
	}
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
