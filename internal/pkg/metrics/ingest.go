package metrics

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// MaxReportObservations bounds one worker report.
	MaxReportObservations = 1024

	// DropReasonWorkerBuffer marks observations a worker discarded because
	// its controller was not taking them.
	DropReasonWorkerBuffer = "workerBuffer"
	// DropReasonStoreQueue marks observations the metrics.db writer had no
	// room for.
	DropReasonStoreQueue = "storeQueue"
	// DropReasonStoreWrite marks observations a metrics.db write lost.
	DropReasonStoreWrite = "storeWrite"

	// maxLabelBytes and maxLabelValues keep a worker from growing the
	// controller's series set. A worker runs agent-chosen code, so its labels
	// are untrusted.
	maxLabelBytes  = 128
	maxLabelValues = 256

	overflowLabel = "other"

	// maxObservationAge and maxObservationFuture bound the time a worker
	// claims, so a forged timestamp cannot dodge retention.
	maxObservationAge    = 24 * time.Hour
	maxObservationFuture = time.Minute
)

// WorkerReport is one batch of a worker's observations. Dropped counts the
// observations the worker lost since its last accepted report.
type WorkerReport struct {
	Observations []Observation `json:"observations"`
	Dropped      int64         `json:"dropped,omitempty"`
}

// labelGuard caps how many distinct values a worker-supplied label can take.
type labelGuard struct {
	mutex sync.Mutex
	seen  map[ObservationKind]map[string]struct{}
}

func (g *labelGuard) admit(kind ObservationKind, value string) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) > maxLabelBytes {
		value = strings.ToValidUTF8(value[:maxLabelBytes], "")
	}

	g.mutex.Lock()
	defer g.mutex.Unlock()

	if g.seen == nil {
		g.seen = map[ObservationKind]map[string]struct{}{}
	}

	values := g.seen[kind]
	if values == nil {
		values = map[string]struct{}{}
		g.seen[kind] = values
	}

	if _, known := values[value]; known {
		return value
	}

	if len(values) >= maxLabelValues {
		return overflowLabel
	}

	values[value] = struct{}{}

	return value
}

// WorkerIngest takes one connected worker's reports. It stamps each
// observation with the identity the connection authenticated, so a worker can
// report only as itself, and it remembers the jobs the worker left running.
type WorkerIngest struct {
	metrics      *Metrics
	sessionID    string
	generationID string
	allSessions  bool

	mutex      sync.Mutex
	activeJobs int64
	closed     bool
}

// NewWorkerIngest binds reports to one authenticated worker generation.
func (m *Metrics) NewWorkerIngest(
	sessionID uuid.UUID,
	generationID uuid.UUID,
) *WorkerIngest {
	return &WorkerIngest{
		metrics:      m,
		sessionID:    sessionID.String(),
		generationID: generationID.String(),
	}
}

// Ingest records one report. A report over the size limit is refused whole,
// and an observation of a kind a worker may not report is skipped.
func (i *WorkerIngest) Ingest(report WorkerReport) error {
	if i == nil || i.metrics == nil {
		return nil
	}

	if len(report.Observations) > MaxReportObservations {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"a worker report may hold %d observations, got %d",
			MaxReportObservations,
			len(report.Observations),
		)
	}

	i.metrics.ObservationsDropped(DropReasonWorkerBuffer, report.Dropped)

	for _, observation := range report.Observations {
		if !workerReportable(observation.Kind) {
			continue
		}

		i.trackJob(observation.Kind)
		i.metrics.Record(i.sanitized(observation))
	}

	return nil
}

// Close ends the worker's jobs on the active-job gauge. A worker that crashed
// reported no completion for them, and its processes died with it.
func (i *WorkerIngest) Close() {
	if i == nil || i.metrics == nil {
		return
	}

	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.closed {
		return
	}

	i.closed = true
	i.metrics.activeJobs.Sub(float64(i.activeJobs))
	i.activeJobs = 0
}

func (i *WorkerIngest) trackJob(kind ObservationKind) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	//nolint:exhaustive // Only job lifecycle kinds move the count.
	switch kind {
	case KindJobStarted:
		i.activeJobs++
	case KindJob:
		i.activeJobs--
	default:
	}
}

func (i *WorkerIngest) sanitized(observation Observation) Observation {
	guard := &i.metrics.labelGuard
	kind := observation.Kind

	observation.SessionID = i.sessionID
	observation.WorkerGenerationID = i.generationID
	observation.TurnID = validUUID(observation.TurnID)
	observation.AgentRunID = validUUID(observation.AgentRunID)
	observation.At = boundedTime(observation.At)
	observation.Name = guard.admit(kind, observation.Name)
	observation.Stage = guard.admit(kind, observation.Stage)
	observation.Function = guard.admit(kind, observation.Function)
	observation.Outcome = boundedOutcome(kind, observation.Outcome)

	observation.DurationMS = max(observation.DurationMS, 0)
	observation.FirstTokenMS = max(observation.FirstTokenMS, 0)
	observation.InputTokens = max(observation.InputTokens, 0)
	observation.OutputTokens = max(observation.OutputTokens, 0)
	observation.CacheReadTokens = max(observation.CacheReadTokens, 0)
	observation.CacheWriteTokens = max(observation.CacheWriteTokens, 0)
	observation.Retries = max(observation.Retries, 0)
	observation.Count = max(observation.Count, 0)
	observation.Bytes = max(observation.Bytes, 0)

	return observation
}

func workerReportable(kind ObservationKind) bool {
	//nolint:exhaustive // Every other kind is recorded by the controller.
	switch kind {
	case KindModel, KindTool, KindHook, KindJobStarted, KindJob,
		KindJobOutputDropped, KindEventDropped, KindWorkerSample:
		return true
	default:
		return false
	}
}

func boundedOutcome(kind ObservationKind, outcome string) string {
	switch outcome {
	case OutcomeSuccess, OutcomeError, OutcomeCancelled:
		return outcome
	}

	//nolint:exhaustive // Only these kinds carry an outcome label.
	switch kind {
	case KindModel, KindTool, KindHook, KindJob:
		return OutcomeError
	default:
		return ""
	}
}

func validUUID(value string) string {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return ""
	}

	return parsed.String()
}

func boundedTime(at time.Time) time.Time {
	now := time.Now()
	if at.Before(now.Add(-maxObservationAge)) ||
		at.After(now.Add(maxObservationFuture)) {
		return now
	}

	return at
}
