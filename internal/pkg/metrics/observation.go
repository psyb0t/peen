package metrics

import (
	"context"
	"time"

	"github.com/psyb0t/ctxscope"
)

// ObservationKind names what one Observation recorded.
type ObservationKind string

const (
	// KindModel is one whole provider request.
	KindModel ObservationKind = "model"
	// KindTool is one tool invocation.
	KindTool ObservationKind = "tool"
	// KindJobStarted is one supervised process starting.
	KindJobStarted ObservationKind = "jobStarted"
	// KindJob is one supervised process ending.
	KindJob ObservationKind = "job"
	// KindJobOutputDropped is job output lines a bounded buffer discarded.
	KindJobOutputDropped ObservationKind = "jobOutputDropped"
	// KindEventDropped is one event a bounded delivery path discarded.
	KindEventDropped ObservationKind = "eventDropped"
	// KindHook is one hook event run for a turn or a tool.
	KindHook ObservationKind = "hook"
	// KindWorkerSample is a periodic reading of a worker's Go runtime.
	KindWorkerSample ObservationKind = "workerSample"
	// KindTurnStarted is one turn admitted to a session.
	KindTurnStarted ObservationKind = "turnStarted"
	// KindTurn is one turn reaching a terminal state.
	KindTurn ObservationKind = "turn"
	// KindAgentRunStarted is one child agent recorded.
	KindAgentRunStarted ObservationKind = "agentRunStarted"
	// KindAgentRun is one child agent reaching a terminal state.
	KindAgentRun ObservationKind = "agentRun"
	// KindCompaction is one stored compaction.
	KindCompaction ObservationKind = "compaction"
	// KindWorkerStarted is one worker launch attempt finishing.
	KindWorkerStarted ObservationKind = "workerStarted"
	// KindWorkerExited is one worker process ending.
	KindWorkerExited ObservationKind = "workerExited"
)

// Observation is one measured fact. It is the unit a worker reports, the
// Prometheus registry counts, and metrics.db stores, so it carries labels,
// identifiers, and numbers only, never prompts, tool output, commands, paths,
// or credentials.
type Observation struct {
	Kind ObservationKind `json:"kind"`
	At   time.Time       `json:"at"`

	SessionID          string `json:"sessionId,omitempty"`
	WorkerGenerationID string `json:"workerGenerationId,omitempty"`
	TurnID             string `json:"turnId,omitempty"`
	AgentRunID         string `json:"agentRunId,omitempty"`

	Name     string `json:"name,omitempty"`
	Stage    string `json:"stage,omitempty"`
	Function string `json:"function,omitempty"`
	Outcome  string `json:"outcome,omitempty"`

	DurationMS       int64 `json:"durationMs,omitempty"`
	FirstTokenMS     int64 `json:"firstTokenMs,omitempty"`
	InputTokens      int64 `json:"inputTokens,omitempty"`
	OutputTokens     int64 `json:"outputTokens,omitempty"`
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
	Retries          int64 `json:"retries,omitempty"`
	Count            int64 `json:"count,omitempty"`
	Bytes            int64 `json:"bytes,omitempty"`
}

// ModelRequest is what ModelCompleted records for one provider request.
type ModelRequest struct {
	Stage            string
	Function         string
	Model            string
	Outcome          string
	Duration         time.Duration
	TimeToFirstToken time.Duration
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Retries          int64
}

// Sink receives every observation a Metrics instance records. It runs on the
// recording goroutine, so it must not block.
type Sink func(Observation)

// scopeIdentity copies the identifiers the context's logging scope carries, so
// a recording call site needs no identifier parameters of its own.
func scopeIdentity(ctx context.Context, observation *Observation) {
	if ctx == nil {
		return
	}

	scope := ctxscope.Get(ctx)
	observation.SessionID = scopeText(scope, scopeSessionID)
	observation.WorkerGenerationID = scopeText(scope, scopeWorkerGenerationID)
	observation.TurnID = scopeText(scope, scopeTurnID)
	observation.AgentRunID = scopeText(scope, scopeAgentRunID)
}

func scopeText(scope ctxscope.Scope, key string) string {
	text, ok := scope[key].(string)
	if !ok {
		return ""
	}

	return text
}
