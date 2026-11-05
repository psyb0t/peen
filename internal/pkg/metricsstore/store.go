// Package metricsstore writes observations to metrics.db and enforces its
// retention. Telemetry is best effort: a write that fails is counted and
// logged, and never reaches the turn that produced it.
package metricsstore

import (
	"context"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

const (
	queueSize     = 8192
	batchSize     = 256
	flushInterval = time.Second
	pruneInterval = time.Hour

	// writeTimeout bounds one batch or prune, including the last flush on
	// shutdown, which runs after the service context has ended.
	writeTimeout = 10 * time.Second
)

// Options configures a Store.
type Options struct {
	Storage   *db.MetricsStorage
	Metrics   *metrics.Metrics
	Retention time.Duration
}

// Store queues observations and writes them in batches on its own goroutine,
// so recording one never waits for SQLite.
type Store struct {
	storage   *db.MetricsStorage
	metrics   *metrics.Metrics
	retention time.Duration
	queue     chan metrics.Observation

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Start begins writing and pruning until Close.
func Start(ctx context.Context, options Options) (*Store, error) {
	if options.Storage == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"metrics storage",
		)
	}

	runCtx, cancel := context.WithCancel(ctx)

	store := &Store{
		storage:   options.Storage,
		metrics:   options.Metrics,
		retention: options.Retention,
		queue:     make(chan metrics.Observation, queueSize),
		cancel:    cancel,
		done:      make(chan struct{}),
	}

	go store.run(runCtx)

	return store, nil
}

// Add queues one observation. A full queue drops it, because telemetry must
// never hold up the work it measures.
func (s *Store) Add(observation metrics.Observation) {
	select {
	case s.queue <- observation:
	default:
		s.metrics.ObservationsDropped(metrics.DropReasonStoreQueue, 1)
	}
}

// Close writes what is queued, stops the writer, and closes metrics.db.
// Repeated calls are safe.
func (s *Store) Close() error {
	var closeErr error

	s.once.Do(func() {
		s.cancel()
		<-s.done

		if err := s.storage.Close(); err != nil {
			closeErr = ctxerrors.Wrap(err, "close metrics storage")
		}
	})

	return closeErr
}

func (s *Store) run(ctx context.Context) {
	defer close(s.done)

	flushes := time.NewTicker(flushInterval)
	defer flushes.Stop()

	prunes := time.NewTicker(pruneInterval)
	defer prunes.Stop()

	s.prune(ctx)

	batch := make([]*models.MetricObservation, 0, batchSize)

	for {
		select {
		case <-ctx.Done():
			s.drain(context.WithoutCancel(ctx), batch)

			return
		case observation := <-s.queue:
			batch = append(batch, row(observation))
			if len(batch) >= batchSize {
				batch = s.write(ctx, batch)
			}
		case <-flushes.C:
			batch = s.write(ctx, batch)
		case <-prunes.C:
			s.prune(ctx)
		}
	}
}

func (s *Store) drain(
	ctx context.Context,
	batch []*models.MetricObservation,
) {
	for {
		select {
		case observation := <-s.queue:
			batch = append(batch, row(observation))
			if len(batch) >= batchSize {
				batch = s.write(ctx, batch)
			}
		default:
			s.write(ctx, batch)

			return
		}
	}
}

// write stores a batch and returns the emptied slice. A failed batch is
// dropped rather than retried, so a broken file cannot grow memory.
func (s *Store) write(
	ctx context.Context,
	batch []*models.MetricObservation,
) []*models.MetricObservation {
	if len(batch) == 0 {
		return batch
	}

	writeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		writeTimeout,
	)
	defer cancel()

	repository := s.storage.Query.MetricObservation

	err := repository.WithContext(writeCtx).CreateInBatches(batch, batchSize)
	if err != nil {
		s.metrics.ObservationsDropped(
			metrics.DropReasonStoreWrite,
			int64(len(batch)),
		)
		ctxscope.GetLogger(ctx).Warn(
			"writing metric observations failed, dropping the batch",
			"reason", "metrics_write_failed",
			"count", len(batch),
			"err", err,
		)
	}

	return batch[:0]
}

// prune deletes observations older than the retention window.
func (s *Store) prune(ctx context.Context) {
	writeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		writeTimeout,
	)
	defer cancel()

	cutoff := time.Now().Add(-s.retention).UnixMilli()
	repository := s.storage.Query.MetricObservation

	result, err := repository.WithContext(writeCtx).
		Where(repository.ObservedAtMs.Lt(cutoff)).
		Delete()
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"pruning metric observations failed",
			"reason", "metrics_prune_failed",
			"err", err,
		)

		return
	}

	ctxscope.GetLogger(ctx).Debug(
		"pruned metric observations",
		"deleted", result.RowsAffected,
	)
}

func row(observation metrics.Observation) *models.MetricObservation {
	return &models.MetricObservation{
		ObservedAtMs:       observation.At.UnixMilli(),
		Kind:               string(observation.Kind),
		SessionID:          observation.SessionID,
		WorkerGenerationID: observation.WorkerGenerationID,
		TurnID:             observation.TurnID,
		AgentRunID:         observation.AgentRunID,
		Name:               observation.Name,
		Stage:              observation.Stage,
		FunctionName:       observation.Function,
		Outcome:            observation.Outcome,
		DurationMs:         observation.DurationMS,
		FirstTokenMs:       observation.FirstTokenMS,
		InputTokens:        observation.InputTokens,
		OutputTokens:       observation.OutputTokens,
		CacheReadTokens:    observation.CacheReadTokens,
		CacheWriteTokens:   observation.CacheWriteTokens,
		Retries:            observation.Retries,
		Count:              observation.Count,
		Bytes:              observation.Bytes,
	}
}
