package tools

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

type pendingJobStop struct {
	job  *Job
	done <-chan struct{}
}

// Shutdown starts bounded graceful stops and waits for durable finalization.
// Cancellation ends this wait. A later call also waits for the prior stop
// goroutines, so their observers and logs cannot outlive resource cleanup.
func (r *JobRegistry) Shutdown(ctx context.Context) error {
	for _, pending := range r.pendingJobShutdown(ctx) {
		select {
		case <-pending.done:
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for job shutdown")
		}

		if pending.job.Snapshot().State == JobStateRunning {
			return ctxerrors.Wrap(
				commerr.ErrFailed,
				"job stop did not complete",
			)
		}
	}

	return nil
}

func (r *JobRegistry) pendingJobShutdown(ctx context.Context) []pendingJobStop {
	r.shutdownMu.Lock()
	defer r.shutdownMu.Unlock()

	if r.shutdownStops == nil {
		r.shutdownStops = make(map[uuid.UUID]pendingJobStop)
	}

	for _, job := range r.List() {
		if job.Snapshot().State != JobStateRunning ||
			r.hasPendingJobStop(job.ID) {
			continue
		}

		done := make(chan struct{})

		r.shutdownStops[job.ID] = pendingJobStop{job: job, done: done}
		go func() {
			defer close(done)
			defer recoverAndLog(ctx, "job shutdown stop")

			job.requestStop(ctx, r.limits.JobStopGracePeriod)
			<-job.Done()
		}()
	}

	result := make([]pendingJobStop, 0, len(r.shutdownStops))
	for _, pending := range r.shutdownStops {
		result = append(result, pending)
	}

	return result
}

func (r *JobRegistry) hasPendingJobStop(id uuid.UUID) bool {
	pending, exists := r.shutdownStops[id]
	if !exists {
		return false
	}

	select {
	case <-pending.done:
		return false
	default:
		return true
	}
}
