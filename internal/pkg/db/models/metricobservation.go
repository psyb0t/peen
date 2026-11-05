package models

// MetricObservation is one row of metrics.db. It holds labels, identifiers,
// and numbers, never message text, tool output, or credentials.
type MetricObservation struct {
	ID                 int64
	ObservedAtMs       int64
	Kind               string
	SessionID          string
	WorkerGenerationID string
	TurnID             string
	AgentRunID         string
	Name               string
	Stage              string
	FunctionName       string
	Outcome            string
	DurationMs         int64
	FirstTokenMs       int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	Retries            int64
	Count              int64
	Bytes              int64
}

// TableName pins the schema-owned metric_observations table name.
func (MetricObservation) TableName() string {
	return "metric_observations"
}
