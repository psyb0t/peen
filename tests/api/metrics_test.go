//go:build integration

package api_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	metricsWorkspacePrefix = "metrics-"
	metricsMessage         = "run a command for the metrics test"
	metricsFinalAnswer     = "the metrics command finished"
	metricsCommand         = "true"
	metricsPurpose         = "api metrics fixture"

	// metricsPollTimeout covers a worker report interval, the metrics.db
	// write interval, and a loaded CI runner.
	metricsPollTimeout = time.Minute
	metricsPollTick    = 250 * time.Millisecond
)

func scrapeMetrics(t *testing.T) string {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		integrationInfra.MetricsURL("/metrics"),
		nil,
	)
	require.NoError(t, err)

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return string(body)
}

// sessionObservations reads metrics.db from a copy of the container's file,
// the way an operator's own tool would read it.
func sessionObservations(
	t *testing.T,
	sessionID string,
) []*models.MetricObservation {
	t.Helper()

	directory := t.TempDir()
	require.NoError(t, integrationInfra.CopyStateFiles(
		t.Context(),
		directory,
		db.MetricsFileName,
		db.MetricsFileName+"-wal",
		db.MetricsFileName+"-shm",
	))

	storage, err := db.OpenMetricsStorage(
		context.Background(),
		db.Config{Directory: filepath.Clean(directory)},
	)
	require.NoError(t, err)

	defer func() { require.NoError(t, storage.Close()) }()

	repository := storage.Query.MetricObservation

	rows, err := repository.WithContext(t.Context()).
		Where(repository.SessionID.Eq(sessionID)).
		Find()
	require.NoError(t, err)

	return rows
}

func observedKinds(rows []*models.MetricObservation) []string {
	kinds := make([]string, 0, len(rows))
	for _, row := range rows {
		kinds = append(kinds, row.Kind)
	}

	return kinds
}

// A worker runs the model loop in its own process, so these numbers exist only
// if its reports reach the controller. Everything here crosses that boundary.
func TestAPIWorkerMetricsReachTheControllerAndMetricsDB(t *testing.T) {
	workspace := openFreshWorkspace(t, metricsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: metricsMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand",
			Arguments: map[string]any{
				"command": metricsCommand,
				"purpose": metricsPurpose,
			},
		}},
		FinalAnswer: metricsFinalAnswer,
	})

	sendSessionMessage(t, connection, workspace.sessionID, metricsMessage)
	readFramesUntilTurnCompleted(t, connection)

	require.Eventually(t, func() bool {
		scraped := scrapeMetrics(t)

		return strings.Contains(scraped, "peen_model_requests_total{") &&
			strings.Contains(scraped, `tool="runCommand"`) &&
			strings.Contains(scraped, "peen_agent_turns_total{")
	}, metricsPollTimeout, metricsPollTick)

	scraped := scrapeMetrics(t)
	assert.Contains(t, scraped, "peen_worker_starts_total{")
	assert.Contains(t, scraped, "peen_agent_jobs_total{")
	assert.Contains(t, scraped, `peen_database_file_bytes{file="metrics"}`)
	assert.Contains(t, scraped, "go_goroutines")

	wanted := []string{
		string(metrics.KindTurn),
		string(metrics.KindModel),
		string(metrics.KindTool),
		string(metrics.KindJob),
	}

	var rows []*models.MetricObservation

	require.Eventually(t, func() bool {
		rows = sessionObservations(t, workspace.sessionID.String())
		kinds := observedKinds(rows)

		for _, kind := range wanted {
			if !slices.Contains(kinds, kind) {
				return false
			}
		}

		return true
	}, metricsPollTimeout, metricsPollTick)

	for _, row := range rows {
		if row.Kind != string(metrics.KindModel) {
			continue
		}

		assert.NotEmpty(t, row.WorkerGenerationID)
		assert.NotEmpty(t, row.TurnID)
		assert.NotEmpty(t, row.Name)
	}
}

func TestMetricsAreOnlyAvailableOnThePrivateListener(t *testing.T) {
	publicResponse := apiRequest(
		t,
		http.MethodGet,
		"/metrics",
		nil,
		authenticatedHeaders(),
	)
	requireAPIStatus(t, publicResponse, http.StatusNotFound)

	metricsRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		integrationInfra.MetricsURL("/metrics"),
		nil,
	)
	require.NoError(t, err)

	privateResponse, err := integrationInfra.HTTPClient().Do(metricsRequest)
	require.NoError(t, err)

	defer func() { require.NoError(t, privateResponse.Body.Close()) }()

	metrics := readAPIResponse(t, privateResponse)
	require.Equal(t, http.StatusOK, metrics.StatusCode)
	assert.Contains(t, string(metrics.Body), "peen_http_requests_total")
}
