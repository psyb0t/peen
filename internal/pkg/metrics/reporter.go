package metrics

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	reportInterval = 5 * time.Second
	sampleInterval = 30 * time.Second

	// reportTimeout bounds one send, so a stuck controller cannot hold a
	// stopping worker.
	reportTimeout = 5 * time.Second

	maxBufferedObservations = 4 * MaxReportObservations
)

// ReportSender delivers one report to the controller.
type ReportSender func(ctx context.Context, report WorkerReport) error

// Reporter is a worker's sink. It buffers observations and sends them to the
// controller in batches, and a controller that is not taking them costs the
// oldest observations rather than a turn.
type Reporter struct {
	send ReportSender

	mutex   sync.Mutex
	pending []Observation
	dropped int64
}

// NewReporter builds a reporter over the worker's controller connection.
func NewReporter(send ReportSender) *Reporter {
	return &Reporter{send: send}
}

// Add buffers one observation. It never blocks on the controller.
func (r *Reporter) Add(observation Observation) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(r.pending) >= maxBufferedObservations {
		r.pending = r.pending[1:]
		r.dropped++
	}

	r.pending = append(r.pending, observation)
}

// Run reports on an interval and samples the worker's Go runtime until ctx
// ends.
func (r *Reporter) Run(ctx context.Context, collector *Metrics) {
	reports := time.NewTicker(reportInterval)
	defer reports.Stop()

	samples := time.NewTicker(sampleInterval)
	defer samples.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reports.C:
			r.report(ctx)
		case <-samples.C:
			collector.recordRuntimeSample(ctx)
		}
	}
}

// Flush sends everything buffered, for a worker that is about to stop.
func (r *Reporter) Flush(ctx context.Context) error {
	for {
		sent, err := r.sendBatch(ctx)
		if err != nil {
			return err
		}

		if !sent {
			return nil
		}
	}
}

func (r *Reporter) report(ctx context.Context) {
	if err := r.Flush(ctx); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"reporting worker metrics failed, keeping them for the next report",
			"reason", "metrics_report_failed",
			"err", err,
		)
	}
}

// sendBatch sends up to one report's worth and restores the batch when the
// send fails.
func (r *Reporter) sendBatch(ctx context.Context) (bool, error) {
	report := r.take()
	if len(report.Observations) == 0 && report.Dropped == 0 {
		return false, nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	if err := r.send(sendCtx, report); err != nil {
		r.restore(report)

		return false, ctxerrors.Wrap(err, "send worker metrics report")
	}

	return true, nil
}

func (r *Reporter) take() WorkerReport {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	size := min(len(r.pending), MaxReportObservations)
	report := WorkerReport{
		Observations: append([]Observation(nil), r.pending[:size]...),
		Dropped:      r.dropped,
	}
	r.pending = r.pending[size:]
	r.dropped = 0

	return report
}

func (r *Reporter) restore(report WorkerReport) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.dropped += report.Dropped

	restored := slices.Concat(report.Observations, r.pending)
	if overflow := len(restored) - maxBufferedObservations; overflow > 0 {
		restored = restored[overflow:]
		r.dropped += int64(overflow)
	}

	r.pending = restored
}

func (m *Metrics) recordRuntimeSample(ctx context.Context) {
	var memory runtime.MemStats

	runtime.ReadMemStats(&memory)

	m.recordScoped(ctx, Observation{
		Kind:  KindWorkerSample,
		Count: int64(runtime.NumGoroutine()),
		Bytes: int64(memory.HeapAlloc), //nolint:gosec // Heap fits in int64.
	})
}
