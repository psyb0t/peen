package control_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryMCPStatusReportsTheLatestTurn(t *testing.T) {
	fixture := newRegistryFixture(t)
	workspace := fixture.workspace(t, "mcp")

	opened, err := fixture.registry.Open(t.Context(), workspace, "")
	require.NoError(t, err)

	sessionID := opened.Session.ID

	empty, err := fixture.registry.MCPStatus(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, api.MCPStatusReport{Servers: []api.MCPServerStatus{}},
		empty)

	for _, payload := range []string{
		`{"servers":[{"name":"old","source":"config","state":"ready",` +
			`"tools":[]}]}`,
		`{"servers":[{"name":"docs","source":"config","transport":"http",` +
			`"state":"ready","tools":["mcp__docs__search"]}]}`,
	} {
		lease, acquireErr := fixture.store.AcquireTurn(t.Context(), sessionID,
			session.StartTurnInput{
				RequestID: uuid.New(), Workspace: workspace,
				Events: []session.EventInput{{
					EventType: agent.EventTypeMCPStatus, PayloadJSON: payload,
				}},
			})
		require.NoError(t, acquireErr)
		require.NoError(t, fixture.store.FinalizeTurn(t.Context(), lease,
			session.FinalizeTurnInput{State: models.TurnStateCompleted}))
	}

	report, err := fixture.registry.MCPStatus(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, report.ReportedAt)

	transport := api.MCPServerStatusTransportHttp
	assert.Equal(t, []api.MCPServerStatus{{
		Name: "docs", Source: api.MCPServerStatusSourceConfig,
		Transport: &transport, State: api.MCPServerStatusStateReady,
		Tools: []string{"mcp__docs__search"},
	}}, report.Servers)

	_, err = fixture.registry.MCPStatus(t.Context(), uuid.New())
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
