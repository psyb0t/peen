package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testForgedSessionID = "forged-session"

// collectedSink keeps what a Metrics instance sends to its sink.
type collectedSink struct {
	mutex        sync.Mutex
	observations []Observation
}

func (c *collectedSink) add(observation Observation) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.observations = append(c.observations, observation)
}

func (c *collectedSink) all() []Observation {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return append([]Observation(nil), c.observations...)
}

type ingestFixture struct {
	collector    *Metrics
	ingest       *WorkerIngest
	sink         *collectedSink
	sessionID    uuid.UUID
	generationID uuid.UUID
}

func newIngestFixture(t *testing.T) ingestFixture {
	t.Helper()

	collector := New()
	sink := &collectedSink{}
	collector.SetSink(sink.add)

	sessionID := uuid.New()
	generationID := uuid.New()

	return ingestFixture{
		collector:    collector,
		ingest:       collector.NewWorkerIngest(sessionID, generationID),
		sink:         sink,
		sessionID:    sessionID,
		generationID: generationID,
	}
}

func TestWorkerReportsCountOnTheControllerRegistry(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)

	require.NoError(t, fixture.ingest.Ingest(WorkerReport{
		Observations: []Observation{
			{
				Kind: KindModel, Name: testModel, Stage: testStage,
				Function: testFunction, Outcome: OutcomeSuccess,
				DurationMS: 1500, FirstTokenMS: 200,
				InputTokens: 10, OutputTokens: 4, CacheReadTokens: 3,
				Retries: 2,
			},
			{Kind: KindTool, Name: testTool, Outcome: OutcomeError},
		},
	}))

	families := metricFamilies(t, fixture.collector)
	labels := map[string]string{
		"service": service, "stage": testStage, "function": testFunction,
		"model": testModel, "outcome": OutcomeSuccess,
	}

	assert.Equal(t, float64(1), metricForLabels(
		t, families, "peen_model_requests_total", labels,
	).GetCounter().GetValue())
	assert.Equal(t, float64(2), metricForLabels(
		t, families, "peen_model_retries_total", labels,
	).GetCounter().GetValue())
	assert.Equal(t, float64(1), metricForLabels(
		t, families, "peen_agent_tool_call_errors_total",
		map[string]string{
			"service": service, "tool": testTool, "outcome": OutcomeError,
		},
	).GetCounter().GetValue())
}

func TestWorkerIdentityComesFromTheConnection(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)
	turnID := uuid.New().String()

	require.NoError(t, fixture.ingest.Ingest(WorkerReport{
		Observations: []Observation{{
			Kind:               KindTool,
			Name:               testTool,
			Outcome:            OutcomeSuccess,
			SessionID:          testForgedSessionID,
			WorkerGenerationID: testForgedSessionID,
			TurnID:             turnID,
			AgentRunID:         "not-a-uuid",
		}},
	}))

	observations := fixture.sink.all()
	require.Len(t, observations, 1)
	assert.Equal(t, fixture.sessionID.String(), observations[0].SessionID)
	assert.Equal(
		t,
		fixture.generationID.String(),
		observations[0].WorkerGenerationID,
	)
	assert.Equal(t, turnID, observations[0].TurnID)
	assert.Empty(t, observations[0].AgentRunID)
}

func TestWorkerCannotReportControllerFacts(t *testing.T) {
	t.Parallel()

	testCases := []ObservationKind{
		KindTurn, KindTurnStarted, KindAgentRun, KindAgentRunStarted,
		KindCompaction, KindWorkerStarted, KindWorkerExited, "invented",
	}

	for _, kind := range testCases {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			fixture := newIngestFixture(t)

			require.NoError(t, fixture.ingest.Ingest(WorkerReport{
				Observations: []Observation{{
					Kind: kind, Outcome: OutcomeSuccess,
				}},
			}))
			assert.Empty(t, fixture.sink.all())
		})
	}
}

func TestWorkerReportOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)

	err := fixture.ingest.Ingest(WorkerReport{
		Observations: make([]Observation, MaxReportObservations+1),
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Empty(t, fixture.sink.all())
}

func TestUntrustedValuesAreBounded(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)
	farFuture := time.Now().Add(time.Hour)

	require.NoError(t, fixture.ingest.Ingest(WorkerReport{
		Observations: []Observation{{
			Kind:        KindTool,
			At:          farFuture,
			Name:        strings.Repeat("x", 2*maxLabelBytes) + "\xff",
			Outcome:     "made-up",
			DurationMS:  -5,
			InputTokens: -5,
		}},
	}))

	observations := fixture.sink.all()
	require.Len(t, observations, 1)
	assert.LessOrEqual(t, len(observations[0].Name), maxLabelBytes)
	assert.Equal(t, OutcomeError, observations[0].Outcome)
	assert.Zero(t, observations[0].DurationMS)
	assert.Zero(t, observations[0].InputTokens)
	assert.True(t, observations[0].At.Before(farFuture))
}

func TestWorkerCannotGrowTheSeriesSetWithoutBound(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)

	for index := range maxLabelValues + 1 {
		require.NoError(t, fixture.ingest.Ingest(WorkerReport{
			Observations: []Observation{{
				Kind:    KindTool,
				Name:    fmt.Sprintf("tool%d", index),
				Outcome: OutcomeSuccess,
			}},
		}))
	}

	observations := fixture.sink.all()
	assert.Equal(t, overflowLabel, observations[len(observations)-1].Name)
}

func TestClosingAWorkerEndsItsJobsOnTheGauge(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)

	require.NoError(t, fixture.ingest.Ingest(WorkerReport{
		Observations: []Observation{
			{Kind: KindJobStarted},
			{Kind: KindJobStarted},
			{Kind: KindJob, Name: "runCommand", Outcome: OutcomeSuccess},
		},
	}))

	activeJobs := func() float64 {
		return metricForLabels(
			t,
			metricFamilies(t, fixture.collector),
			"peen_agent_jobs_active",
			nil,
		).GetGauge().GetValue()
	}
	require.Equal(t, float64(1), activeJobs())

	fixture.ingest.Close()
	fixture.ingest.Close()

	assert.Zero(t, activeJobs())
}

func TestReportedDropsAreCounted(t *testing.T) {
	t.Parallel()

	fixture := newIngestFixture(t)

	require.NoError(t, fixture.ingest.Ingest(WorkerReport{Dropped: 3}))

	assert.Equal(t, float64(3), metricForLabels(
		t, metricFamilies(t, fixture.collector),
		"peen_metrics_observations_dropped_total",
		map[string]string{
			"service": service, "reason": DropReasonWorkerBuffer,
		},
	).GetCounter().GetValue())
}

func TestNilIngestIsSafe(t *testing.T) {
	t.Parallel()

	var ingest *WorkerIngest

	require.NoError(t, ingest.Ingest(WorkerReport{}))
	ingest.Close()
}
