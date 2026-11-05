// Package metrics owns Peen's application Prometheus registry and its bounded
// runtime collectors.
package metrics

import (
	"context"
	"database/sql"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const (
	namespace = "peen"
	service   = "peen"

	labelService   = "service"
	labelMethod    = "method"
	labelRoute     = "route"
	labelStatus    = "status"
	labelOutcome   = "outcome"
	labelOperation = "operation"
	labelTable     = "table"
	labelState     = "state"
	labelStage     = "stage"
	labelFunction  = "function"
	labelModel     = "model"
	labelKind      = "kind"
	labelTool      = "tool"
	labelStream    = "stream"
	labelReason    = "reason"
	labelVersion   = "version"
	labelRevision  = "revision"
	labelEvent     = "event"
	labelScope     = "scope"
	labelFile      = "file"

	scopeSessionID          = "session_id"
	scopeWorkerGenerationID = "worker_generation_id"
	scopeTurnID             = "turn_id"
	scopeAgentRunID         = "agent_run_id"

	subsystemHTTP     = "http"
	subsystemDatabase = "database"
	subsystemModel    = "model"
	subsystemAgent    = "agent"
	subsystemEvents   = "events"
	subsystemWorker   = "worker"
	subsystemMetrics  = "metrics"

	tokenKindInput      = "input"
	tokenKindOutput     = "output"
	tokenKindCacheRead  = "cacheRead"
	tokenKindCacheWrite = "cacheWrite"

	outcomeSuccess   = "success"
	outcomeError     = "error"
	outcomeCancelled = "cancelled"

	unknownBuildValue = "unknown"

	buildSettingVCSRevision = "vcs.revision"

	poolStateOpen      = "open"
	poolStateInUse     = "in_use"
	poolStateIdle      = "idle"
	poolStateWaitCount = "wait_count"
)

// OutcomeSuccess names a completed operation without an error.
const OutcomeSuccess = outcomeSuccess

// OutcomeError names an operation which returned an error.
const OutcomeError = outcomeError

// OutcomeCancelled names an operation ended by cancellation.
const OutcomeCancelled = outcomeCancelled

// Metrics is the application-owned Prometheus registry. Its methods accept
// only labels that Peen controls, never request, session, workspace, command,
// path, prompt, or SQL values.
type Metrics struct {
	collectors []prometheus.Collector
	registry   *prometheus.Registry

	buildInfo *prometheus.GaugeVec

	httpRequests  *prometheus.CounterVec
	httpDuration  *prometheus.HistogramVec
	httpErrors    *prometheus.CounterVec
	httpInFlight  prometheus.Gauge
	databaseCalls *prometheus.CounterVec
	databaseTime  *prometheus.HistogramVec
	databaseError *prometheus.CounterVec
	databasePool  *prometheus.GaugeVec

	modelCalls  *prometheus.CounterVec
	modelTime   *prometheus.HistogramVec
	modelErrors *prometheus.CounterVec
	modelTTFT   *prometheus.HistogramVec
	modelTokens *prometheus.CounterVec

	toolCalls  *prometheus.CounterVec
	toolTime   *prometheus.HistogramVec
	toolErrors *prometheus.CounterVec

	jobCalls    *prometheus.CounterVec
	jobTime     *prometheus.HistogramVec
	jobErrors   *prometheus.CounterVec
	activeJobs  prometheus.Gauge
	outputDrops *prometheus.CounterVec
	eventDrops  *prometheus.CounterVec

	modelRetries *prometheus.CounterVec

	activity activityCollectors

	observationDrops *prometheus.CounterVec

	sink atomic.Pointer[Sink]

	labelGuard labelGuard
}

// New constructs one isolated application registry. It intentionally does
// not use Prometheus's global registry, so tests and embedding programs cannot
// leak collectors into each other.
func New() *Metrics {
	instance := newMetrics(prometheus.NewRegistry())
	version, revision := buildIdentity()
	instance.buildInfo.WithLabelValues(version, revision).Set(1)

	// The Go collector stays out of the owned collector list, so an embedding
	// program that registers this instance keeps its own runtime collector.
	instance.registry.MustRegister(collectors.NewGoCollector())

	return instance
}

// Registry returns the registry owned by this Metrics instance.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}

	return m.registry
}

// Describe exposes the owned collectors to an embedding caller's registry.
func (m *Metrics) Describe(descriptions chan<- *prometheus.Desc) {
	for _, collector := range m.collectors {
		collector.Describe(descriptions)
	}
}

// Collect forwards current values without using Prometheus's global registry.
func (m *Metrics) Collect(values chan<- prometheus.Metric) {
	for _, collector := range m.collectors {
		collector.Collect(values)
	}
}

// HTTPStarted marks one public HTTP request in flight.
func (m *Metrics) HTTPStarted() {
	if m == nil {
		return
	}

	m.httpInFlight.Inc()
}

// HTTPCompleted records one public HTTP request and removes it from the
// in-flight gauge. route must be Echo's templated route, never a raw URL.
func (m *Metrics) HTTPCompleted(
	method string,
	route string,
	status string,
	outcome string,
	duration time.Duration,
) {
	if m == nil {
		return
	}

	labels := prometheus.Labels{
		labelService: service,
		labelMethod:  method,
		labelRoute:   route,
		labelStatus:  status,
		labelOutcome: outcome,
	}
	m.httpRequests.With(labels).Inc()
	m.httpDuration.With(labels).Observe(duration.Seconds())

	if outcome == OutcomeError {
		m.httpErrors.With(labels).Inc()
	}

	m.httpInFlight.Dec()
}

// DatabaseCompleted records one GORM operation. table is reduced to Peen's
// fixed schema vocabulary before becoming a label.
func (m *Metrics) DatabaseCompleted(
	operation string,
	table string,
	outcome string,
	duration time.Duration,
) {
	if m == nil {
		return
	}

	labels := prometheus.Labels{
		labelService:   service,
		labelOperation: operation,
		labelTable:     boundedTable(table),
		labelOutcome:   outcome,
	}
	m.databaseCalls.With(labels).Inc()
	m.databaseTime.With(labels).Observe(duration.Seconds())

	if outcome == OutcomeError {
		m.databaseError.With(labels).Inc()
	}
}

// DatabasePool records a point-in-time SQLite pool snapshot.
func (m *Metrics) DatabasePool(stats sql.DBStats) {
	if m == nil {
		return
	}

	m.databasePool.WithLabelValues(service, poolStateOpen).Set(
		float64(stats.OpenConnections),
	)
	m.databasePool.WithLabelValues(service, poolStateInUse).Set(
		float64(stats.InUse),
	)
	m.databasePool.WithLabelValues(service, poolStateIdle).Set(
		float64(stats.Idle),
	)
	m.databasePool.WithLabelValues(service, poolStateWaitCount).Set(
		float64(stats.WaitCount),
	)
}

// ModelCompleted records one whole Elelem request. Token counts are the
// request's final run totals, already accumulated by Elelem across rounds.
func (m *Metrics) ModelCompleted(ctx context.Context, request ModelRequest) {
	m.recordScoped(ctx, Observation{
		Kind:             KindModel,
		Name:             request.Model,
		Stage:            request.Stage,
		Function:         request.Function,
		Outcome:          request.Outcome,
		DurationMS:       request.Duration.Milliseconds(),
		FirstTokenMS:     request.TimeToFirstToken.Milliseconds(),
		InputTokens:      request.InputTokens,
		OutputTokens:     request.OutputTokens,
		CacheReadTokens:  request.CacheReadTokens,
		CacheWriteTokens: request.CacheWriteTokens,
		Retries:          request.Retries,
	})
}

// ToolCompleted records one host tool invocation.
func (m *Metrics) ToolCompleted(
	ctx context.Context,
	tool string,
	outcome string,
	duration time.Duration,
) {
	m.recordScoped(ctx, Observation{
		Kind:       KindTool,
		Name:       tool,
		Outcome:    outcome,
		DurationMS: duration.Milliseconds(),
	})
}

// HookCompleted records one hook event run. event is a harness hook event
// name, a closed set.
func (m *Metrics) HookCompleted(
	ctx context.Context,
	event string,
	outcome string,
	duration time.Duration,
) {
	m.recordScoped(ctx, Observation{
		Kind:       KindHook,
		Name:       event,
		Outcome:    outcome,
		DurationMS: duration.Milliseconds(),
	})
}

// JobStarted increments the number of supervised process jobs currently live.
func (m *Metrics) JobStarted(ctx context.Context) {
	m.recordScoped(ctx, Observation{Kind: KindJobStarted})
}

// JobCompleted records one supervised process result and removes it from the
// active-job gauge.
func (m *Metrics) JobCompleted(
	ctx context.Context,
	operation string,
	outcome string,
	duration time.Duration,
) {
	m.recordScoped(ctx, Observation{
		Kind:       KindJob,
		Name:       operation,
		Outcome:    outcome,
		DurationMS: duration.Milliseconds(),
	})
}

// JobOutputDropped records lines discarded from a completed job's bounded
// stdout or stderr buffer.
func (m *Metrics) JobOutputDropped(
	ctx context.Context,
	stream string,
	count int,
) {
	if count <= 0 {
		return
	}

	m.recordScoped(ctx, Observation{
		Kind:  KindJobOutputDropped,
		Name:  stream,
		Count: int64(count),
	})
}

// EventDropped records a durable pending-event or live-subscriber drop.
func (m *Metrics) EventDropped(reason string) {
	m.Record(Observation{Kind: KindEventDropped, Name: reason, Count: 1})
}

// ObservationsDropped records observations that never reached metrics.db or
// the controller. reason is one of the DropReason constants.
func (m *Metrics) ObservationsDropped(reason string, count int64) {
	if m == nil || count <= 0 {
		return
	}

	m.observationDrops.WithLabelValues(service, reason).Add(float64(count))
}

// SetSink installs where every recorded observation goes next. A worker
// forwards to its controller, and the controller stores to metrics.db.
func (m *Metrics) SetSink(sink Sink) {
	if m == nil {
		return
	}

	if sink == nil {
		m.sink.Store(nil)

		return
	}

	m.sink.Store(&sink)
}

// Record counts one observation and passes it to the sink. A missing At is
// the current time.
func (m *Metrics) Record(observation Observation) {
	if m == nil {
		return
	}

	if observation.At.IsZero() {
		observation.At = time.Now()
	}

	m.apply(observation)

	if sink := m.sink.Load(); sink != nil {
		(*sink)(observation)
	}
}

func (m *Metrics) recordScoped(ctx context.Context, observation Observation) {
	if m == nil {
		return
	}

	scopeIdentity(ctx, &observation)
	m.Record(observation)
}

//nolint:funlen // All collectors are registered together in the owned registry.
func newMetrics(registry *prometheus.Registry) *Metrics {
	instance := &Metrics{registry: registry}
	instance.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Build identity for this Peen process.",
	}, []string{labelVersion, labelRevision})
	instance.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemHTTP,
		Name:      "requests_total",
		Help:      "Completed public HTTP requests.",
	}, []string{
		labelService,
		labelMethod,
		labelRoute,
		labelStatus,
		labelOutcome,
	})
	instance.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemHTTP,
		Name:      "request_duration_seconds",
		Help:      "Public HTTP request duration in seconds.",
	}, []string{
		labelService,
		labelMethod,
		labelRoute,
		labelStatus,
		labelOutcome,
	})
	instance.httpErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemHTTP,
		Name:      "request_errors_total",
		Help:      "Public HTTP requests that completed with an error.",
	}, []string{
		labelService,
		labelMethod,
		labelRoute,
		labelStatus,
		labelOutcome,
	})
	instance.httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystemHTTP,
		Name:      "requests_in_flight",
		Help:      "Public HTTP requests currently in flight.",
	})
	instance.databaseCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemDatabase,
		Name:      "operations_total",
		Help:      "Completed database operations.",
	}, []string{labelService, labelOperation, labelTable, labelOutcome})
	instance.databaseTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemDatabase,
		Name:      "operation_duration_seconds",
		Help:      "Database operation duration in seconds.",
	}, []string{labelService, labelOperation, labelTable, labelOutcome})
	instance.databaseError = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemDatabase,
		Name:      "operation_errors_total",
		Help:      "Database operations that completed with an error.",
	}, []string{labelService, labelOperation, labelTable, labelOutcome})
	instance.databasePool = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystemDatabase,
		Name:      "pool_connections",
		Help:      "SQLite connection pool state.",
	}, []string{labelService, labelState})
	instance.modelCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "requests_total",
		Help:      "Completed model requests.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
	})
	instance.modelTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "request_duration_seconds",
		Help:      "Model request duration in seconds.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
	})
	instance.modelErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "request_errors_total",
		Help:      "Model requests that completed with an error.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
	})
	instance.modelTTFT = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "time_to_first_token_seconds",
		Help:      "Time to first model delta in seconds.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
	})
	instance.modelTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "tokens_total",
		Help:      "Reported model tokens by kind.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
		labelKind,
	})
	instance.toolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "tool_calls_total",
		Help:      "Completed host tool calls.",
	}, []string{labelService, labelTool, labelOutcome})
	instance.toolTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "tool_call_duration_seconds",
		Help:      "Host tool call duration in seconds.",
	}, []string{labelService, labelTool, labelOutcome})
	instance.toolErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "tool_call_errors_total",
		Help:      "Host tool calls that completed with an error.",
	}, []string{labelService, labelTool, labelOutcome})
	instance.jobCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "jobs_total",
		Help:      "Completed supervised process jobs.",
	}, []string{labelService, labelOperation, labelOutcome})
	instance.jobTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "job_duration_seconds",
		Help:      "Supervised process job duration in seconds.",
	}, []string{labelService, labelOperation, labelOutcome})
	instance.jobErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "job_errors_total",
		Help:      "Supervised process jobs that completed with an error.",
	}, []string{labelService, labelOperation, labelOutcome})
	instance.activeJobs = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "jobs_active",
		Help:      "Supervised process jobs currently active.",
	})
	instance.outputDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemAgent,
		Name:      "job_output_dropped_lines_total",
		Help:      "Job output lines dropped by bounded buffers.",
	}, []string{labelService, labelStream})
	instance.eventDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemEvents,
		Name:      "dropped_total",
		Help:      "Events dropped by bounded delivery paths.",
	}, []string{labelService, labelReason})
	instance.modelRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystemModel,
		Name:      "retries_total",
		Help:      "Failed provider attempts retried within a request.",
	}, []string{
		labelService,
		labelStage,
		labelFunction,
		labelModel,
		labelOutcome,
	})
	instance.observationDrops = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystemMetrics,
			Name:      "observations_dropped_total",
			Help:      "Observations lost before reaching metrics.db.",
		},
		[]string{labelService, labelReason},
	)
	instance.activity = newActivityCollectors()

	instance.collectors = []prometheus.Collector{
		instance.buildInfo,
		instance.httpRequests,
		instance.httpDuration,
		instance.httpErrors,
		instance.httpInFlight,
		instance.databaseCalls,
		instance.databaseTime,
		instance.databaseError,
		instance.databasePool,
		instance.modelCalls,
		instance.modelTime,
		instance.modelErrors,
		instance.modelTTFT,
		instance.modelTokens,
		instance.toolCalls,
		instance.toolTime,
		instance.toolErrors,
		instance.jobCalls,
		instance.jobTime,
		instance.jobErrors,
		instance.activeJobs,
		instance.outputDrops,
		instance.eventDrops,
		instance.modelRetries,
		instance.observationDrops,
	}
	instance.collectors = append(
		instance.collectors,
		instance.activity.all()...,
	)
	registry.MustRegister(instance.collectors...)

	return instance
}

func buildIdentity() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownBuildValue, unknownBuildValue
	}

	version := info.Main.Version
	if version == "" {
		version = unknownBuildValue
	}

	revision := unknownBuildValue

	for _, setting := range info.Settings {
		if setting.Key == buildSettingVCSRevision && setting.Value != "" {
			revision = setting.Value

			break
		}
	}

	return version, revision
}

func boundedTable(table string) string {
	switch table {
	case "sessions", "messages", "events", "turns", "context_snapshots",
		"prompt_snapshots", "compactions":
		return table
	default:
		return unknownBuildValue
	}
}
