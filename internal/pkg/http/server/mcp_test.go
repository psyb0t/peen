package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mcpTestPath = "/session/mcp"

func TestServerGetSessionMCPStatus(t *testing.T) {
	t.Parallel()

	reportedAt := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	failure := "connect: executable file not found"
	transport := api.MCPServerStatusTransportStdio

	testCases := []struct {
		name     string
		registry *testSessionRegistry
		status   int
	}{
		{
			name: "reported servers",
			registry: &testSessionRegistry{mcpReport: api.MCPStatusReport{
				ReportedAt: &reportedAt,
				Servers: []api.MCPServerStatus{
					{
						Name: "docs", Source: api.MCPServerStatusSourceConfig,
						State: api.MCPServerStatusStateReady,
						Tools: []string{"mcp__docs__search"},
					},
					{
						Name: "local", Source: api.MCPServerStatusSourceConfig,
						State: api.MCPServerStatusStateFailed, Tools: []string{},
						Error: &failure, Transport: &transport,
					},
				},
			}},
			status: http.StatusOK,
		},
		{
			name: "no report yet",
			registry: &testSessionRegistry{mcpReport: api.MCPStatusReport{
				Servers: []api.MCPServerStatus{},
			}},
			status: http.StatusOK,
		},
		{
			name: "missing session",
			registry: &testSessionRegistry{
				mcpErr: ctxerrors.Wrap(commerr.ErrNotFound, "find session"),
			},
			status: http.StatusNotFound,
		},
		{
			name: "store failure",
			registry: &testSessionRegistry{
				mcpErr: errors.New("disk on fire"), //nolint:err113 // Test cause.
			},
			status: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := serveLearnedRequest(t, tc.registry, http.MethodGet,
				mcpTestPath, nil)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())

			if tc.status != http.StatusOK {
				assert.NotContains(t, recorder.Body.String(), "disk on fire")

				return
			}

			var report api.MCPStatusReport
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &report))
			assert.Equal(t, tc.registry.mcpReport, report)
		})
	}
}
