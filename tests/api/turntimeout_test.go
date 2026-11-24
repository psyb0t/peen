//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"testing"

	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPITurnTimeout(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}

		ctx, cancel := context.WithTimeout(
			context.WithoutCancel(t.Context()), requestTimeout)
		defer cancel()

		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("timeout failure container logs: %s", logs)
	})

	workspace := openFreshWorkspace(t, "turn-timeout-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	event := dabluveees.NewEvent(apiTestWebSocketMessageSend,
		map[string]any{"message": "wait for the deadline", "timeoutSeconds": 1}).
		SetMetadata(apiTestWebSocketSessionIDParameter, workspace.sessionID.String())
	assertValidMessageSend(t, event)
	require.NoError(t, connection.WriteJSON(event))
	var cancelled, failed bool
	for !cancelled || !failed {
		frame := readWebSocketEvent(t, connection)
		switch string(frame.Type) {
		case "turn.cancelled":
			var result struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(frame.Data, &result))
			assert.Equal(t, "timeout", result.Reason)
			cancelled = true
		case "message.failed":
			var result wsapi.MessageFailedData
			require.NoError(t, json.Unmarshal(frame.Data, &result))
			assert.Equal(t, server.ErrorCodeTurnTimeout, string(result.Code))
			failed = true
		}
	}
}
