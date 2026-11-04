package control

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// mcpStatusData is the data of an mcp.status event.
type mcpStatusData struct {
	Servers []api.MCPServerStatus `json:"servers"`
}

// MCPStatus returns the MCP servers the session's worker reported at the
// start of its most recent turn. Before any report the server list is empty
// and ReportedAt is nil.
func (r *Registry) MCPStatus(
	ctx context.Context,
	sessionID uuid.UUID,
) (api.MCPStatusReport, error) {
	page, err := r.store.ListEvents(ctx, sessionID, session.ListEventsOptions{
		Limit:      1,
		Order:      session.PageOrderDescending,
		EventTypes: []string{agent.EventTypeMCPStatus},
	})
	if err != nil {
		return api.MCPStatusReport{}, ctxerrors.Wrap(err,
			"read the latest MCP status")
	}

	report := api.MCPStatusReport{Servers: []api.MCPServerStatus{}}
	if len(page.Items) == 0 {
		return report, nil
	}

	latest := page.Items[0]

	var data mcpStatusData
	if err := json.Unmarshal([]byte(latest.PayloadJSON), &data); err != nil {
		return api.MCPStatusReport{}, ctxerrors.Wrap(err,
			"decode the latest MCP status")
	}

	if data.Servers != nil {
		report.Servers = data.Servers
	}

	reportedAt := latest.CreatedAt.UTC()
	report.ReportedAt = &reportedAt

	return report, nil
}
