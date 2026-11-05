package metrics

import (
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/ctxerrors"
)

const (
	walFileSuffix = "-wal"

	// activityBucketStartSeconds and activityBucketCount give duration
	// histograms a range from one second to a little over two hours, because
	// a turn or a child agent can run far longer than an HTTP request.
	activityBucketStartSeconds = 1
	activityBucketFactor       = 2
	activityBucketCount        = 14

	compactionScopeSession  = "session"
	compactionScopeAgentRun = "agentRun"

	millisecondsPerSecond = 1000
)

// activityCollectors counts what the controller's durable records prove:
// turns, child agents, compactions, and workers, plus hook runs.
type activityCollectors struct {
	turnsStarted     *prometheus.CounterVec
	turns            *prometheus.CounterVec
	turnTime         *prometheus.HistogramVec
	agentRunsStarted *prometheus.CounterVec
	agentRuns        *prometheus.CounterVec
	agentRunTime     *prometheus.HistogramVec
	compactions      *prometheus.CounterVec
	compactionTokens *prometheus.CounterVec
	hooks            *prometheus.CounterVec
	hookTime         *prometheus.HistogramVec
	workerStarts     *prometheus.CounterVec
	workerStartTime  *prometheus.HistogramVec
	workerExits      *prometheus.CounterVec
	workersActive    prometheus.Gauge
}

func activityCounter(
	subsystem string,
	name string,
	help string,
	labels ...string,
) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      name,
		Help:      help,
	}, append([]string{labelService}, labels...))
}

func activityHistogram(
	subsystem string,
	name string,
	help string,
	labels ...string,
) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      name,
		Help:      help,
		Buckets: prometheus.ExponentialBuckets(
			activityBucketStartSeconds,
			activityBucketFactor,
			activityBucketCount,
		),
	}, append([]string{labelService}, labels...))
}

func newActivityCollectors() activityCollectors {
	return activityCollectors{
		turnsStarted: activityCounter(subsystemAgent, "turns_started_total",
			"Turns admitted to a session."),
		turns: activityCounter(subsystemAgent, "turns_total",
			"Turns that reached a terminal state.", labelOutcome),
		turnTime: activityHistogram(subsystemAgent, "turn_duration_seconds",
			"Turn duration from admission to terminal state.", labelOutcome),
		agentRunsStarted: activityCounter(subsystemAgent,
			"agent_runs_started_total", "Child agents recorded."),
		agentRuns: activityCounter(subsystemAgent, "agent_runs_total",
			"Child agents that reached a terminal state.", labelOutcome),
		agentRunTime: activityHistogram(subsystemAgent,
			"agent_run_duration_seconds",
			"Child agent duration from start to terminal state.",
			labelOutcome),
		compactions: activityCounter(subsystemAgent, "compactions_total",
			"Stored compactions.", labelScope),
		compactionTokens: activityCounter(subsystemAgent,
			"compaction_tokens_total",
			"Tokens summarized and tokens in the stored summaries.",
			labelScope, labelKind),
		hooks: activityCounter(subsystemAgent, "hook_runs_total",
			"Hook event runs.", labelEvent, labelOutcome),
		hookTime: activityHistogram(subsystemAgent,
			"hook_run_duration_seconds", "Hook event run duration.",
			labelEvent, labelOutcome),
		workerStarts: activityCounter(subsystemWorker, "starts_total",
			"Worker launch attempts.", labelKind, labelOutcome),
		workerStartTime: activityHistogram(subsystemWorker,
			"start_duration_seconds",
			"Time from launch to worker registration.", labelKind),
		workerExits: activityCounter(subsystemWorker, "exits_total",
			"Worker processes that ended.", labelOutcome),
		workersActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystemWorker,
			Name:      "active",
			Help:      "Worker processes currently registered.",
		}),
	}
}

func (a activityCollectors) all() []prometheus.Collector {
	return []prometheus.Collector{
		a.turnsStarted, a.turns, a.turnTime,
		a.agentRunsStarted, a.agentRuns, a.agentRunTime,
		a.compactions, a.compactionTokens,
		a.hooks, a.hookTime,
		a.workerStarts, a.workerStartTime, a.workerExits, a.workersActive,
	}
}

// apply updates the Prometheus series one observation feeds.
func (m *Metrics) apply(observation Observation) {
	if m.applyAgentWork(observation) {
		return
	}

	m.applyActivity(observation)
}

//nolint:exhaustive // The other kinds belong to applyActivity.
func (m *Metrics) applyAgentWork(observation Observation) bool {
	switch observation.Kind {
	case KindModel:
		m.applyModel(observation)
	case KindTool:
		m.applyTool(observation)
	case KindJobStarted:
		m.activeJobs.Inc()
	case KindJob:
		m.applyJob(observation)
	case KindJobOutputDropped:
		m.outputDrops.WithLabelValues(service, observation.Name).
			Add(float64(observation.Count))
	case KindEventDropped:
		m.eventDrops.WithLabelValues(service, observation.Name).
			Add(float64(observation.Count))
	default:
		return false
	}

	return true
}

//nolint:exhaustive // The other kinds belong to applyAgentWork.
func (m *Metrics) applyActivity(observation Observation) {
	activity := m.activity
	outcome := observation.Outcome
	seconds := observationSeconds(observation)

	switch observation.Kind {
	case KindHook:
		activity.hooks.WithLabelValues(service, observation.Name, outcome).Inc()
		activity.hookTime.WithLabelValues(service, observation.Name, outcome).
			Observe(seconds)
	case KindTurnStarted:
		activity.turnsStarted.WithLabelValues(service).Inc()
	case KindTurn:
		activity.turns.WithLabelValues(service, outcome).Inc()
		activity.turnTime.WithLabelValues(service, outcome).Observe(seconds)
	case KindAgentRunStarted:
		activity.agentRunsStarted.WithLabelValues(service).Inc()
	case KindAgentRun:
		activity.agentRuns.WithLabelValues(service, outcome).Inc()
		activity.agentRunTime.WithLabelValues(service, outcome).Observe(seconds)
	case KindCompaction:
		m.applyCompaction(observation)
	case KindWorkerStarted:
		m.applyWorkerStarted(observation, seconds)
	case KindWorkerExited:
		activity.workerExits.WithLabelValues(service, outcome).Inc()
		activity.workersActive.Dec()
	default:
	}
}

func (m *Metrics) applyModel(observation Observation) {
	labels := []string{
		service,
		observation.Stage,
		observation.Function,
		observation.Name,
		observation.Outcome,
	}
	seconds := observationSeconds(observation)

	m.modelCalls.WithLabelValues(labels...).Inc()
	m.modelTime.WithLabelValues(labels...).Observe(seconds)

	if observation.Outcome == OutcomeError {
		m.modelErrors.WithLabelValues(labels...).Inc()
	}

	if observation.FirstTokenMS > 0 {
		m.modelTTFT.WithLabelValues(labels...).Observe(
			float64(observation.FirstTokenMS) / millisecondsPerSecond,
		)
	}

	if observation.Retries > 0 {
		m.modelRetries.WithLabelValues(labels...).
			Add(float64(observation.Retries))
	}

	tokenKinds := map[string]int64{
		tokenKindInput:      observation.InputTokens,
		tokenKindOutput:     observation.OutputTokens,
		tokenKindCacheRead:  observation.CacheReadTokens,
		tokenKindCacheWrite: observation.CacheWriteTokens,
	}
	for kind, tokens := range tokenKinds {
		m.modelTokens.WithLabelValues(
			service,
			observation.Stage,
			observation.Function,
			observation.Name,
			observation.Outcome,
			kind,
		).Add(float64(tokens))
	}
}

func (m *Metrics) applyTool(observation Observation) {
	labels := []string{service, observation.Name, observation.Outcome}
	m.toolCalls.WithLabelValues(labels...).Inc()
	m.toolTime.WithLabelValues(labels...).
		Observe(observationSeconds(observation))

	if observation.Outcome == OutcomeError {
		m.toolErrors.WithLabelValues(labels...).Inc()
	}
}

func (m *Metrics) applyJob(observation Observation) {
	labels := []string{service, observation.Name, observation.Outcome}
	m.jobCalls.WithLabelValues(labels...).Inc()
	m.jobTime.WithLabelValues(labels...).
		Observe(observationSeconds(observation))

	if observation.Outcome == OutcomeError {
		m.jobErrors.WithLabelValues(labels...).Inc()
	}

	m.activeJobs.Dec()
}

func (m *Metrics) applyCompaction(observation Observation) {
	activity := m.activity
	scope := compactionScopeSession

	if observation.AgentRunID != "" {
		scope = compactionScopeAgentRun
	}

	activity.compactions.WithLabelValues(service, scope).Inc()
	activity.compactionTokens.WithLabelValues(service, scope, tokenKindInput).
		Add(float64(observation.InputTokens))
	activity.compactionTokens.WithLabelValues(service, scope, tokenKindOutput).
		Add(float64(observation.OutputTokens))
}

func (m *Metrics) applyWorkerStarted(observation Observation, seconds float64) {
	activity := m.activity
	activity.workerStarts.
		WithLabelValues(service, observation.Name, observation.Outcome).Inc()

	if observation.Outcome != OutcomeSuccess {
		return
	}

	activity.workerStartTime.WithLabelValues(service, observation.Name).
		Observe(seconds)
	activity.workersActive.Inc()
}

func observationSeconds(observation Observation) float64 {
	return float64(observation.DurationMS) / millisecondsPerSecond
}

// WatchFiles publishes each named database file's size, write-ahead log
// included, on every scrape.
func (m *Metrics) WatchFiles(paths map[string]string) error {
	if m == nil {
		return nil
	}

	for name, path := range paths {
		gauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   namespace,
			Subsystem:   subsystemDatabase,
			Name:        "file_bytes",
			Help:        "Size of a database file and its write-ahead log.",
			ConstLabels: prometheus.Labels{labelFile: name},
		}, func() float64 {
			return float64(fileBytes(path) + fileBytes(path+walFileSuffix))
		})
		if err := m.registry.Register(gauge); err != nil {
			return ctxerrors.Wrapf(err, "register %s file size", name)
		}
	}

	return nil
}

// fileBytes reads zero for a file that does not exist yet, which is a normal
// state for a write-ahead log.
func fileBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}

	return info.Size()
}
