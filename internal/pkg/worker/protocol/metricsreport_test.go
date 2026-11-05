package protocol

import (
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsReportIsStampedWithTheConnectionIdentity(t *testing.T) {
	t.Parallel()

	controller, sessionID := newControllerFixture(t)
	collector := metrics.New()
	observations := []metrics.Observation{}

	collector.SetSink(func(observation metrics.Observation) {
		observations = append(observations, observation)
	})

	controller.metrics = collector.NewWorkerIngest(
		sessionID, controller.generationID,
	)

	_, err := controller.HandleCall(
		context.Background(),
		string(MethodReportMetrics),
		mustEncode(t, metrics.WorkerReport{
			Observations: []metrics.Observation{{
				Kind:      metrics.KindTool,
				Name:      "readFile",
				Outcome:   metrics.OutcomeSuccess,
				SessionID: "another-session",
			}},
		}),
	)
	require.NoError(t, err)

	require.Len(t, observations, 1)
	assert.Equal(t, sessionID.String(), observations[0].SessionID)
	assert.Equal(
		t,
		controller.generationID.String(),
		observations[0].WorkerGenerationID,
	)
}

func TestMetricsReportOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	controller, sessionID := newControllerFixture(t)
	controller.metrics = metrics.New().NewWorkerIngest(
		sessionID, controller.generationID,
	)

	_, err := controller.HandleCall(
		context.Background(),
		string(MethodReportMetrics),
		mustEncode(t, metrics.WorkerReport{
			Observations: make(
				[]metrics.Observation,
				metrics.MaxReportObservations+1,
			),
		}),
	)

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestMetricsReportWithoutARegistryIsAccepted(t *testing.T) {
	t.Parallel()

	controller, _ := newControllerFixture(t)

	_, err := controller.HandleCall(
		context.Background(),
		string(MethodReportMetrics),
		mustEncode(t, metrics.WorkerReport{}),
	)

	require.NoError(t, err)
}
