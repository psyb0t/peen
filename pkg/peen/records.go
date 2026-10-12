package peen

import "time"

type AgentRun struct {
	AgentRunID            string             `json:"agentRunId"`
	AllowedTools          []string           `json:"allowedTools"`
	CancelRequested       bool               `json:"cancelRequested"`
	CompletionTokenCount  int64              `json:"completionTokenCount"`
	Definition            AgentRunDefinition `json:"definition"`
	Depth                 int64              `json:"depth"`
	EndedAt               *time.Time         `json:"endedAt,omitempty"`
	EventCount            int64              `json:"eventCount"`
	FailureClassification string             `json:"failureClassification"`
	FailureDetail         string             `json:"failureDetail"`
	FinishReason          string             `json:"finishReason"`
	Instructions          string             `json:"instructions"`
	Model                 string             `json:"model"`
	ModelReference        string             `json:"modelReference"`
	Name                  string             `json:"name"`
	ParentAgentRunID      *string            `json:"parentAgentRunId,omitempty"`
	ParentToolCallID      *string            `json:"parentToolCallId,omitempty"`
	ParentTurnID          string             `json:"parentTurnId"`
	PromptTokenCount      int64              `json:"promptTokenCount"`
	Question              *string            `json:"question,omitempty"`
	RequestID             string             `json:"requestId"`
	ResponseMessages      []any              `json:"responseMessages"`
	ResponseText          string             `json:"responseText"`
	ResponseThinking      string             `json:"responseThinking"`
	SessionID             string             `json:"sessionId"`
	StartedAt             time.Time          `json:"startedAt"`
	State                 AgentRunState      `json:"state"`
	SystemPrompt          string             `json:"systemPrompt"`
	Task                  string             `json:"task"`
	Waiting               bool               `json:"waiting"`

	WorkerGenerationID *string `json:"workerGenerationId,omitempty"`
	Workspace          string  `json:"workspace"`
}

type AgentRunCancelResponse struct {
	AgentRunID      string        `json:"agentRunId"`
	CancelRequested bool          `json:"cancelRequested"`
	State           AgentRunState `json:"state"`
}

type AgentRunCompaction struct {
	AgentRunID         string    `json:"agentRunId"`
	CreatedAt          time.Time `json:"createdAt"`
	DirectFromSequence int64     `json:"directFromSequence"`
	DirectToSequence   int64     `json:"directToSequence"`
	FromMessageID      string    `json:"fromMessageId"`
	FromSequence       int64     `json:"fromSequence"`
	ID                 string    `json:"id"`
	InputTokenCount    int64     `json:"inputTokenCount"`
	Model              string    `json:"model"`
	ParentCompactionID *string   `json:"parentCompactionId,omitempty"`
	PromptHash         string    `json:"promptHash"`
	SessionID          string    `json:"sessionId"`
	SourceMessageCount int64     `json:"sourceMessageCount"`
	Summary            string    `json:"summary"`
	SummaryTokenCount  int64     `json:"summaryTokenCount"`
	ToMessageID        string    `json:"toMessageId"`
	ToSequence         int64     `json:"toSequence"`
}

type AgentRunCompactionPage struct {
	Compactions []AgentRunCompaction `json:"compactions"`
	HasMore     bool                 `json:"hasMore"`
	Limit       int32                `json:"limit"`
	Offset      int32                `json:"offset"`
}

type AgentRunEvent struct {
	CreatedAt time.Time       `json:"createdAt"`
	Payload   *map[string]any `json:"payload,omitempty"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
}

type AgentRunEventPage struct {
	AgentRunID string          `json:"agentRunId"`
	Events     []AgentRunEvent `json:"events"`
	HasMore    bool            `json:"hasMore"`
	NextCursor int64           `json:"nextCursor"`
	State      AgentRunState   `json:"state"`
}

type AgentRunMessage struct {
	AgentRunID   string            `json:"agentRunId"`
	CompactionID *string           `json:"compactionId,omitempty"`
	Content      string            `json:"content"`
	CreatedAt    time.Time         `json:"createdAt"`
	ID           string            `json:"id"`
	Incomplete   bool              `json:"incomplete"`
	IsError      bool              `json:"isError"`
	Model        *string           `json:"model,omitempty"`
	Role         MessageRole       `json:"role"`
	Sequence     int64             `json:"sequence"`
	SessionID    string            `json:"sessionId"`
	Thinking     *string           `json:"thinking,omitempty"`
	ToolCallID   *string           `json:"toolCallId,omitempty"`
	ToolCalls    *[]map[string]any `json:"toolCalls,omitempty"`
}

type AgentRunMessagePage struct {
	HasMore  bool              `json:"hasMore"`
	Limit    int32             `json:"limit"`
	Messages []AgentRunMessage `json:"messages"`
	Offset   int32             `json:"offset"`
}

type AgentRunPage struct {
	Agents  []AgentRun `json:"agents"`
	HasMore bool       `json:"hasMore"`
	Limit   int32      `json:"limit"`
	Offset  int32      `json:"offset"`
}

type Compaction struct {
	CreatedAt          time.Time `json:"createdAt"`
	FromMessageID      string    `json:"fromMessageId"`
	FromSequence       int64     `json:"fromSequence"`
	ID                 string    `json:"id"`
	InputTokenCount    int64     `json:"inputTokenCount"`
	Model              string    `json:"model"`
	ParentCompactionID *string   `json:"parentCompactionId,omitempty"`
	PromptHash         string    `json:"promptHash"`
	SessionID          string    `json:"sessionId"`
	SourceMessageCount int64     `json:"sourceMessageCount"`
	Summary            string    `json:"summary"`
	SummaryTokenCount  int64     `json:"summaryTokenCount"`
	ToMessageID        string    `json:"toMessageId"`
	ToSequence         int64     `json:"toSequence"`
}

type CompactionPage struct {
	Compactions []Compaction `json:"compactions"`
	HasMore     bool         `json:"hasMore"`
	Limit       int32        `json:"limit"`
	Offset      int32        `json:"offset"`
}

type ContextSnapshot struct {
	CreatedAt       time.Time              `json:"createdAt"`
	Hash            string                 `json:"hash"`
	Manifest        []ContextManifestEntry `json:"manifest"`
	ResolvedContent string                 `json:"resolvedContent"`
}

type Job struct {
	Command            string     `json:"command"`
	Directory          string     `json:"directory"`
	EndedAt            *time.Time `json:"endedAt,omitempty"`
	ExitCode           int64      `json:"exitCode"`
	FailureDetail      string     `json:"failureDetail"`
	JobID              string     `json:"jobId"`
	PID                int64      `json:"pid"`
	Purpose            string     `json:"purpose"`
	SessionID          string     `json:"sessionId"`
	StartedAt          time.Time  `json:"startedAt"`
	State              JobState   `json:"state"`
	ToolCallID         *string    `json:"toolCallId,omitempty"`
	TurnID             string     `json:"turnId"`
	WorkerGenerationID *string    `json:"workerGenerationId,omitempty"`
}

type JobOutput struct {
	HasMore    bool            `json:"hasMore"`
	JobID      string          `json:"jobId"`
	Lines      []JobOutputLine `json:"lines"`
	NextCursor int64           `json:"nextCursor"`
	State      JobState        `json:"state"`
}

type JobOutputLine struct {
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
	JobID     string    `json:"jobId"`
	Sequence  int64     `json:"sequence"`
	SessionID string    `json:"sessionId"`
	Stream    JobStream `json:"stream"`
}

type JobPage struct {
	HasMore bool  `json:"hasMore"`
	Jobs    []Job `json:"jobs"`
	Limit   int32 `json:"limit"`
	Offset  int32 `json:"offset"`
}

type JobSignalRecord struct {
	Accepted       bool      `json:"accepted"`
	ID             string    `json:"id"`
	JobID          string    `json:"jobId"`
	RequestedAt    time.Time `json:"requestedAt"`
	SessionID      string    `json:"sessionId"`
	Signal         JobSignal `json:"signal"`
	StateAtRequest JobState  `json:"stateAtRequest"`
}

type JobSignalRecordPage struct {
	HasMore        bool              `json:"hasMore"`
	Job            Job               `json:"job"`
	Limit          int32             `json:"limit"`
	Offset         int32             `json:"offset"`
	SignalRequests []JobSignalRecord `json:"signalRequests"`
}

type JobSignalResponse struct {
	JobID     string   `json:"jobId"`
	Signalled bool     `json:"signalled"`
	State     JobState `json:"state"`
}

type ModelCall struct {
	BilledCostAmount        string           `json:"billedCostAmount"`
	CacheReadTokens         int64            `json:"cacheReadTokens"`
	CacheWriteLongTTLTokens int64            `json:"cacheWriteLongTtlTokens"`
	CacheWriteTokens        int64            `json:"cacheWriteTokens"`
	CompletedAt             *time.Time       `json:"completedAt,omitempty"`
	CompletionTokens        int64            `json:"completionTokens"`
	CostKnown               bool             `json:"costKnown"`
	FailureClassification   string           `json:"failureClassification"`
	FailureDetail           string           `json:"failureDetail"`
	FinishReason            string           `json:"finishReason"`
	ID                      string           `json:"id"`
	ModelRunID              string           `json:"modelRunId"`
	PromptTokens            int64            `json:"promptTokens"`
	ReasoningTokens         int64            `json:"reasoningTokens"`
	RequestMessages         []any            `json:"requestMessages"`
	RequestTools            []any            `json:"requestTools"`
	ResponseCostAmount      string           `json:"responseCostAmount"`
	ResponseMessage         *map[string]any  `json:"responseMessage"`
	ResponseModelID         string           `json:"responseModelId"`
	ResponseUsage           map[string]any   `json:"responseUsage"`
	RetryAttemptCount       int64            `json:"retryAttemptCount"`
	RetryAttempts           []map[string]any `json:"retryAttempts"`
	RetryCostAmount         string           `json:"retryCostAmount"`
	Round                   int64            `json:"round"`
	SessionID               string           `json:"sessionId"`
	StartedAt               time.Time        `json:"startedAt"`
	State                   ModelCallState   `json:"state"`
	TotalAttempts           int64            `json:"totalAttempts"`
	TotalTokens             int64            `json:"totalTokens"`
	WastedCompletionTokens  int64            `json:"wastedCompletionTokens"`
	WastedPromptTokens      int64            `json:"wastedPromptTokens"`
	WastedTotalTokens       int64            `json:"wastedTotalTokens"`
}

type ModelCallPage struct {
	Calls    []ModelCall `json:"calls"`
	HasMore  bool        `json:"hasMore"`
	Limit    int32       `json:"limit"`
	ModelRun ModelRun    `json:"modelRun"`
	Offset   int32       `json:"offset"`
}

type ModelRun struct {
	AgentRunID            *string        `json:"agentRunId,omitempty"`
	BilledCostAmount      string         `json:"billedCostAmount"`
	CompletedAt           *time.Time     `json:"completedAt,omitempty"`
	ConnectionName        string         `json:"connectionName"`
	CostKnown             bool           `json:"costKnown"`
	FailureClassification string         `json:"failureClassification"`
	FailureDetail         string         `json:"failureDetail"`
	FinishReason          string         `json:"finishReason"`
	ID                    string         `json:"id"`
	ModelReference        string         `json:"modelReference"`
	RequestSettings       map[string]any `json:"requestSettings"`
	RequestedModelID      string         `json:"requestedModelId"`
	ResponseCostAmount    string         `json:"responseCostAmount"`
	ResponseInjections    []any          `json:"responseInjections"`
	ResponseMessages      []any          `json:"responseMessages"`
	ResponseModelID       string         `json:"responseModelId"`
	ResponseText          string         `json:"responseText"`
	ResponseThinking      string         `json:"responseThinking"`
	ResponseUsage         map[string]any `json:"responseUsage"`
	RetryCostAmount       string         `json:"retryCostAmount"`
	SessionID             string         `json:"sessionId"`
	Stage                 ModelRunStage  `json:"stage"`
	StartedAt             time.Time      `json:"startedAt"`
	State                 ModelRunState  `json:"state"`
	TurnID                string         `json:"turnId"`
}

type ModelRunPage struct {
	HasMore   bool       `json:"hasMore"`
	Limit     int32      `json:"limit"`
	ModelRuns []ModelRun `json:"modelRuns"`
	Offset    int32      `json:"offset"`
}

type PromptSnapshot struct {
	CreatedAt       time.Time `json:"createdAt"`
	EffectivePrompt string    `json:"effectivePrompt"`
	Hash            string    `json:"hash"`
}

type SessionNotice struct {
	CreatedAt time.Time       `json:"createdAt"`
	Data      *map[string]any `json:"data,omitempty"`
	Delivery  Delivery        `json:"delivery"`
	ID        string          `json:"id"`
	Source    string          `json:"source"`
	Summary   string          `json:"summary"`
	Type      string          `json:"type"`
}

type SessionNoticePage struct {
	HasMore bool            `json:"hasMore"`
	Limit   int32           `json:"limit"`
	Notices []SessionNotice `json:"notices"`
	Offset  int32           `json:"offset"`
}

type TranscriptEvent struct {
	CreatedAt          time.Time      `json:"createdAt"`
	ID                 string         `json:"id"`
	ParentToolCallID   *string        `json:"parentToolCallId,omitempty"`
	Payload            map[string]any `json:"payload"`
	RequestID          string         `json:"requestId"`
	Sequence           int64          `json:"sequence"`
	SessionID          string         `json:"sessionId"`
	TurnID             string         `json:"turnId"`
	Type               string         `json:"type"`
	WorkerGenerationID *string        `json:"workerGenerationId,omitempty"`
}

type TranscriptEventPage struct {
	Events  []TranscriptEvent `json:"events"`
	HasMore bool              `json:"hasMore"`
	Limit   int32             `json:"limit"`
	Offset  int32             `json:"offset"`
}

type Turn struct {
	CancelRequested       bool       `json:"cancelRequested"`
	CompletedAt           *time.Time `json:"completedAt,omitempty"`
	ContextSnapshotHash   *string    `json:"contextSnapshotHash,omitempty"`
	FailureClassification string     `json:"failureClassification"`
	ID                    string     `json:"id"`
	PromptSnapshotHash    *string    `json:"promptSnapshotHash,omitempty"`
	RequestID             string     `json:"requestId"`
	SessionID             string     `json:"sessionId"`
	StartedAt             time.Time  `json:"startedAt"`
	State                 TurnState  `json:"state"`
	Workspace             string     `json:"workspace"`
}

type TurnPage struct {
	HasMore bool   `json:"hasMore"`
	Limit   int32  `json:"limit"`
	Offset  int32  `json:"offset"`
	Turns   []Turn `json:"turns"`
}

// ContextManifestEntry identifies one harness source used by a turn.
type ContextManifestEntry struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Priority int    `json:"priority"`
	Hash     string `json:"hash"`
}
