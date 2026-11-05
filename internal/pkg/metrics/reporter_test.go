package metrics

import (
	"context"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSender keeps delivered reports and fails while failing is set.
type recordingSender struct {
	mutex   sync.Mutex
	reports []WorkerReport
	failing bool
}

func (s *recordingSender) send(
	_ context.Context,
	report WorkerReport,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.failing {
		return commerr.ErrUnavailable
	}

	s.reports = append(s.reports, report)

	return nil
}

func (s *recordingSender) delivered() (int, int64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	count := 0

	var dropped int64

	for _, report := range s.reports {
		count += len(report.Observations)
		dropped += report.Dropped
	}

	return count, dropped
}

func TestFlushSendsEverythingInBoundedReports(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	reporter := NewReporter(sender.send)
	total := MaxReportObservations + 1

	for range total {
		reporter.Add(Observation{Kind: KindTool})
	}

	require.NoError(t, reporter.Flush(t.Context()))

	count, _ := sender.delivered()
	assert.Equal(t, total, count)

	for _, report := range sender.reports {
		assert.LessOrEqual(t, len(report.Observations), MaxReportObservations)
	}
}

func TestFailedReportKeepsItsObservations(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{failing: true}
	reporter := NewReporter(sender.send)
	reporter.Add(Observation{Kind: KindTool, Name: testTool})

	require.ErrorIs(t, reporter.Flush(t.Context()), commerr.ErrUnavailable)

	sender.failing = false
	require.NoError(t, reporter.Flush(t.Context()))

	count, _ := sender.delivered()
	assert.Equal(t, 1, count)
}

func TestFullBufferDropsTheOldestAndReportsTheCount(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	reporter := NewReporter(sender.send)

	for range maxBufferedObservations {
		reporter.Add(Observation{Kind: KindTool, Name: "old"})
	}

	reporter.Add(Observation{Kind: KindTool, Name: testTool})

	require.NoError(t, reporter.Flush(t.Context()))

	count, dropped := sender.delivered()
	assert.Equal(t, maxBufferedObservations, count)
	assert.Equal(t, int64(1), dropped)

	last := sender.reports[len(sender.reports)-1]
	assert.Equal(t, testTool, last.Observations[len(last.Observations)-1].Name)
}

func TestRecordedObservationsCarryTheScopeIdentity(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	reporter := NewReporter(sender.send)
	collector := New()
	collector.SetSink(reporter.Add)

	ctx := ctxscope.Set(
		t.Context(),
		ctxscope.Attr(scopeTurnID, "turn"),
		ctxscope.Attr(scopeAgentRunID, "run"),
	)
	collector.ToolCompleted(ctx, testTool, OutcomeSuccess, testDuration)

	require.NoError(t, reporter.Flush(t.Context()))
	require.Len(t, sender.reports, 1)
	require.Len(t, sender.reports[0].Observations, 1)

	observation := sender.reports[0].Observations[0]
	assert.Equal(t, "turn", observation.TurnID)
	assert.Equal(t, "run", observation.AgentRunID)
	assert.False(t, observation.At.IsZero())
}

func TestRuntimeSampleReadsTheGoRuntime(t *testing.T) {
	t.Parallel()

	sink := &collectedSink{}
	collector := New()
	collector.SetSink(sink.add)

	collector.recordRuntimeSample(t.Context())

	observations := sink.all()
	require.Len(t, observations, 1)
	assert.Equal(t, KindWorkerSample, observations[0].Kind)
	assert.Positive(t, observations[0].Count)
	assert.Positive(t, observations[0].Bytes)
}

func TestClearingTheSinkStopsDelivery(t *testing.T) {
	t.Parallel()

	sink := &collectedSink{}
	collector := New()
	collector.SetSink(sink.add)
	collector.SetSink(nil)

	collector.EventDropped("pending")

	assert.Empty(t, sink.all())
}
