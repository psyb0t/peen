package agent

import (
	"context"
	"maps"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	metricsTestInputTokens  = 11
	metricsTestOutputTokens = 7
)

func TestRuntimeRunRecordsModelMetric(t *testing.T) {
	collector := metrics.New()
	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
		func(options *RuntimeOptions) {
			options.Metrics = collector
		},
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "record model metrics",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	families, err := collector.Registry().Gather()
	require.NoError(t, err)
	assert.Equal(
		t,
		float64(1),
		agentMetricCounterValue(t, families, "peen_model_requests_total", map[string]string{
			"service":  "peen",
			"stage":    modelMetricStageTurn,
			"function": modelMetricFunctionRunProvider,
			"model":    runtimeTestModelReference,
			"outcome":  metrics.OutcomeSuccess,
		}),
	)
}

func TestObserveModelRequestRecordsCancellationAndTokens(t *testing.T) {
	collector := metrics.New()
	startedAt := time.Now().Add(-time.Second)
	response := &elelem.Response{Usage: elelem.Usage{
		TokenCounts: elelem.TokenCounts{
			Prompt:     metricsTestInputTokens,
			Completion: metricsTestOutputTokens,
		},
	}}

	observeModelRequest(
		collector,
		modelMetricStageChild,
		modelMetricFunctionRunChild,
		runtimeTestModelReference,
		startedAt,
		time.Time{},
		response,
		context.Canceled,
	)

	families, err := collector.Registry().Gather()
	require.NoError(t, err)
	labels := map[string]string{
		"service":  "peen",
		"stage":    modelMetricStageChild,
		"function": modelMetricFunctionRunChild,
		"model":    runtimeTestModelReference,
		"outcome":  metrics.OutcomeCancelled,
	}
	assert.Equal(
		t,
		float64(1),
		agentMetricCounterValue(t, families, "peen_model_requests_total", labels),
	)
	assert.Equal(
		t,
		float64(metricsTestInputTokens),
		agentMetricCounterValue(
			t,
			families,
			"peen_model_tokens_total",
			withMetricLabel(labels, "kind", "input"),
		),
	)
	assert.Equal(
		t,
		float64(metricsTestOutputTokens),
		agentMetricCounterValue(
			t,
			families,
			"peen_model_tokens_total",
			withMetricLabel(labels, "kind", "output"),
		),
	)
}

func agentMetricCounterValue(
	t *testing.T,
	families []*dto.MetricFamily,
	name string,
	wantLabels map[string]string,
) float64 {
	t.Helper()

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

		for _, metric := range family.Metric {
			if !hasMetricLabels(metric, wantLabels) {
				continue
			}

			require.NotNil(t, metric.Counter)

			return metric.Counter.GetValue()
		}
	}

	require.Failf(t, "metric not found", "%s with labels %v", name, wantLabels)

	return 0
}

func hasMetricLabels(metric *dto.Metric, wantLabels map[string]string) bool {
	if len(metric.Label) != len(wantLabels) {
		return false
	}

	for _, label := range metric.Label {
		if wantLabels[label.GetName()] != label.GetValue() {
			return false
		}
	}

	return true
}

func withMetricLabel(
	labels map[string]string,
	name string,
	value string,
) map[string]string {
	withLabel := make(map[string]string, len(labels)+1)
	maps.Copy(withLabel, labels)
	withLabel[name] = value

	return withLabel
}
