package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	emseepee "github.com/psyb0t/aichteeteapee/serbewr/em-see-pee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPServerPolicyDefaultsAndControllerBoundary(t *testing.T) {
	t.Parallel()

	policy, err := (Config{}).MCPServerPolicy()
	require.NoError(t, err)
	assert.Equal(t, []string{"127.0.0.1", "::1", "localhost"}, policy.Hosts())
	assert.Equal(t, []string{"http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"}, policy.Origins())
	for _, key := range []string{"PEEN_MCP_SERVER", "PEEN_MCP_SERVER_ALLOWED_HOSTS", "PEEN_MCP_SERVER_ALLOWED_ORIGINS"} {
		assert.True(t, workerEnvironmentReserved(key))
		assert.False(t, workerRuntimeEnvironmentKey(key))
		assert.False(t, controlWorkerEnvironmentKey(key, []string{key}))
	}
}

func TestMCPServerPolicyRejectsMalformedConfiguration(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"*", "http://localhost", "localhost:8080", "localhost,", "a..b", "-host", "fe80::1%eth0", strings.Repeat("x", 254), strings.Repeat("x,", 128) + "x"} {
		t.Run("host "+host, func(t *testing.T) {
			t.Parallel()
			_, err := (Config{MCPServerAllowedHosts: host}).MCPServerPolicy()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
	for _, origin := range []string{"*", "null", "http://user@localhost", "http://localhost/path", "http://localhost?query", "http://localhost#fragment", "file://localhost", "http://localhost:", "http://localhost:0", "http://[0:0::1]"} {
		t.Run("origin "+origin, func(t *testing.T) {
			t.Parallel()
			_, err := (Config{MCPServerAllowedOrigins: origin}).MCPServerPolicy()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
	policy, err := (Config{MCPServerAllowedHosts: "Peen.Example", MCPServerAllowedOrigins: "HTTPS://PEEN.EXAMPLE"}).MCPServerPolicy()
	require.NoError(t, err)
	assert.Equal(t, []string{"peen.example"}, policy.Hosts())
	assert.Equal(t, []string{"https://peen.example"}, policy.Origins())
}

// Every host and origin startup accepts must be one the MCP route accepts,
// and a request naming it as written must pass the route's checks. Peen and
// the route each normalize the values, so this holds the two together.
func TestMCPServerPolicyAgreesWithTheMCPRoute(t *testing.T) {
	t.Parallel()

	protocol := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	for _, tc := range []struct {
		host      string
		authority string
		origin    string
	}{
		{host: "localhost", authority: "localhost:8080", origin: "http://localhost:8080"},
		{host: "Peen.Example", authority: "PEEN.example:443", origin: "HTTPS://Peen.Example"},
		{host: "127.0.0.1", authority: "127.0.0.1", origin: "http://127.0.0.1:9000"},
		{host: "[::1]", authority: "[::1]:8080", origin: "http://[::1]:8080"},
		{host: "2001:db8::1", authority: "[2001:db8::1]:8443", origin: "https://[2001:DB8::1]:8443"},
		{host: "xn--bcher-kva.example", authority: "xn--bcher-kva.example", origin: "https://xn--bcher-kva.example"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			policy, err := (Config{MCPServerAllowedHosts: tc.host, MCPServerAllowedOrigins: tc.origin}).MCPServerPolicy()
			require.NoError(t, err)
			route, err := emseepee.NewRoute("/mcp",
				func(*http.Request) *mcp.Server { return protocol },
				emseepee.WithAllowedHosts(policy.Hosts()...),
				emseepee.WithAllowedOrigins(policy.Origins()...))
			require.NoError(t, err)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/mcp", nil)
			request.Host = tc.authority
			request.Header.Set("Origin", tc.origin)
			response := httptest.NewRecorder()
			route.Handler(response, request)
			assert.Equal(t, http.StatusMethodNotAllowed, response.Code, response.Body.String())
		})
	}
}
