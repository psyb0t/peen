package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPServerPolicyDefaultsAndControllerBoundary(t *testing.T) {
	t.Parallel()

	policy, err := (Config{}).MCPServerPolicy()
	require.NoError(t, err)
	for _, host := range []string{"localhost:8080", "127.0.0.1:1234", "[::1]:8080"} {
		assert.True(t, policy.AllowsHost(host), host)
	}
	for _, host := range []string{"attacker.example", "localhost.attacker", "localhost:bad", "localhost:+80", "localhost:", "localhost:70000"} {
		assert.False(t, policy.AllowsHost(host), host)
	}
	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		assert.True(t, policy.AllowsOrigin(origin), origin)
	}
	for _, origin := range []string{"null", "http://localhost", "http://localhost:8080/", "https://attacker.example", "http://localhost:8080#"} {
		assert.False(t, policy.AllowsOrigin(origin), origin)
	}
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

	for _, host := range []string{"*", "http://localhost", "localhost:8080", "localhost,", "a..b", "-host", strings.Repeat("x", 254), strings.Repeat("x,", 128) + "x"} {
		t.Run("host "+host, func(t *testing.T) {
			t.Parallel()
			_, err := (Config{MCPServerAllowedHosts: host}).MCPServerPolicy()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
	for _, origin := range []string{"*", "null", "http://user@localhost", "http://localhost/path", "http://localhost?query", "http://localhost#fragment", "file://localhost", "http://localhost:", "http://localhost:0"} {
		t.Run("origin "+origin, func(t *testing.T) {
			t.Parallel()
			_, err := (Config{MCPServerAllowedOrigins: origin}).MCPServerPolicy()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
	policy, err := (Config{MCPServerAllowedHosts: "Peen.Example", MCPServerAllowedOrigins: "https://peen.example"}).MCPServerPolicy()
	require.NoError(t, err)
	assert.Equal(t, []string{"peen.example"}, policy.Hosts())
	assert.Equal(t, []string{"https://peen.example"}, policy.Origins())
	assert.True(t, policy.AllowsHost("peen.example:443"))
	assert.True(t, policy.AllowsOrigin("https://PEEN.EXAMPLE"))
	assert.False(t, policy.AllowsOrigin("http://peen.example"))
}
