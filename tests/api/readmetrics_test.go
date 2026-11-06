//go:build integration

package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	readMetricsWorkspacePrefix = "read-metrics-"
	readMetricsFirstMessage    = "run a command so there is something to measure"
	readMetricsSecondMessage   = "now read the metrics for this session"
	readMetricsFinalAnswer     = "the metrics were read"
	readMetricsToolName        = "readMetrics"
)

// An agent reads its numbers across the worker socket. The second turn's tool
// result proves the query reached metrics.db and saw the first turn's command.
func TestAPIReadMetricsReturnsThisSessionsToolTotals(t *testing.T) {
	workspace := openFreshWorkspace(t, readMetricsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: readMetricsFirstMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand",
			Arguments: map[string]any{
				"command": metricsCommand,
				"purpose": metricsPurpose,
			},
		}},
		FinalAnswer: metricsFinalAnswer,
	})

	sendSessionMessage(
		t, connection, workspace.sessionID, readMetricsFirstMessage,
	)
	readFramesUntilTurnCompleted(t, connection)

	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: readMetricsSecondMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name:      readMetricsToolName,
			Arguments: map[string]any{"summary": "tools"},
		}},
		FinalAnswer: readMetricsFinalAnswer,
	})

	sendSessionMessage(
		t, connection, workspace.sessionID, readMetricsSecondMessage,
	)

	frames := readFramesUntilTurnCompleted(t, connection)

	var transcript strings.Builder

	for _, frame := range frames {
		encoded, err := json.Marshal(frame.Data)
		require.NoError(t, err)

		transcript.Write(encoded)
	}

	assert.Contains(t, transcript.String(), "runCommand")
	assert.Contains(t, transcript.String(), "totalDurationMs")
}
