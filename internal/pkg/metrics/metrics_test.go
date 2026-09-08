package metrics

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testModel        = "aigate/test-model"
	testRoute        = "/v1/messages"
	testStatusOK     = "200"
	testTool         = "read_file"
	testOperation    = "query"
	testFunction     = "run_provider"
	testStage        = "turn"
	testDuration     = time.Millisecond
	testInputTokens  = 12
	testOutputTokens = 34
)

func TestMetricsRecordsBoundedOperationFamilies(t *testing.T) {
	t.Parallel()

	instance := New()
	instance.HTTPStarted()
	instance.HTTPCompleted(
		http.MethodGet,
		testRoute,
		testStatusOK,
		OutcomeSuccess,
		testDuration,
	)
	instance.DatabaseCompleted(
		testOperation,
		"unrecognized_table",
		OutcomeError,
		testDuration,
	)
	instance.DatabasePool(sql.DBStats{OpenConnections: 2, InUse: 1, Idle: 1})
	instance.ModelCompleted(
		testStage,
		testFunction,
		testModel,
		OutcomeSuccess,
		testDuration,
		testDuration,
		testInputTokens,
		testOutputTokens,
	)
	instance.ToolCompleted(testTool, OutcomeSuccess, testDuration)
	instance.JobStarted()
	instance.JobCompleted("run_command", OutcomeSuccess, testDuration)
	instance.JobOutputDropped("stdout", 3)
	instance.EventDropped("pending")

	families := metricFamilies(t, instance)
	assert.Equal(
		t,
		float64(1),
		metricForLabels(t, families, "peen_http_requests_total", map[string]string{
			"service": service,
			"method":  http.MethodGet,
			"route":   testRoute,
			"status":  testStatusOK,
			"outcome": OutcomeSuccess,
		}).GetCounter().GetValue(),
	)
	assert.Equal(
		t,
		float64(1),
		metricForLabels(t, families, "peen_database_operation_errors_total", map[string]string{
			"service":   service,
			"operation": testOperation,
			"table":     unknownBuildValue,
			"outcome":   OutcomeError,
		}).GetCounter().GetValue(),
	)
	assert.Equal(
		t,
		float64(testInputTokens),
		metricForLabels(t, families, "peen_model_tokens_total", map[string]string{
			"service":  service,
			"stage":    testStage,
			"function": testFunction,
			"model":    testModel,
			"outcome":  OutcomeSuccess,
			"kind":     "input",
		}).GetCounter().GetValue(),
	)
	assert.Equal(
		t,
		float64(testOutputTokens),
		metricForLabels(t, families, "peen_model_tokens_total", map[string]string{
			"service":  service,
			"stage":    testStage,
			"function": testFunction,
			"model":    testModel,
			"outcome":  OutcomeSuccess,
			"kind":     "output",
		}).GetCounter().GetValue(),
	)
	assert.Equal(
		t,
		float64(0),
		metricForLabels(t, families, "peen_agent_jobs_active", nil).GetGauge().GetValue(),
	)
	assert.Equal(
		t,
		float64(3),
		metricForLabels(t, families, "peen_agent_job_output_dropped_lines_total", map[string]string{
			"service": service,
			"stream":  "stdout",
		}).GetCounter().GetValue(),
	)
	assert.Equal(
		t,
		float64(1),
		metricForLabels(t, families, "peen_events_dropped_total", map[string]string{
			"service": service,
			"reason":  "pending",
		}).GetCounter().GetValue(),
	)
}

func metricFamilies(t *testing.T, instance *Metrics) map[string]*dto.MetricFamily {
	t.Helper()

	families, err := instance.Registry().Gather()
	require.NoError(t, err)

	byName := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		byName[family.GetName()] = family
	}

	return byName
}

func metricForLabels(
	t *testing.T,
	families map[string]*dto.MetricFamily,
	name string,
	wantLabels map[string]string,
) *dto.Metric {
	t.Helper()

	family, ok := families[name]
	require.True(t, ok, "missing metric family %q", name)

	for _, metric := range family.Metric {
		if labelsMatch(metric.Label, wantLabels) {
			return metric
		}
	}

	require.Failf(t, "missing labeled metric", "%s %v", name, wantLabels)

	return nil
}

func labelsMatch(labels []*dto.LabelPair, want map[string]string) bool {
	if len(labels) != len(want) {
		return false
	}

	for _, label := range labels {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}

	return true
}
