package models

import (
	"time"

	"github.com/google/uuid"
)

// ModelCall records one outbound provider round within a logical ModelRun.
type ModelCall struct {
	ID                      uuid.UUID
	SessionID               uuid.UUID
	ModelRunID              uuid.UUID
	Round                   int64
	RequestMessagesJSON     string
	RequestToolsJSON        string
	State                   ModelCallState
	ResponseMessageJSON     string
	ResponseUsageJSON       string
	RetryAttemptsJSON       string
	RetryAttemptCount       int64
	PromptTokens            int64
	CompletionTokens        int64
	TotalTokens             int64
	ReasoningTokens         int64
	CacheReadTokens         int64
	CacheWriteTokens        int64
	CacheWriteLongTTLTokens int64
	WastedPromptTokens      int64
	WastedCompletionTokens  int64
	WastedTotalTokens       int64
	TotalAttempts           int64
	ResponseModelID         string
	FinishReason            string
	ResponseCostAmount      string
	RetryCostAmount         string
	BilledCostAmount        string
	CostKnown               bool
	FailureClassification   string
	FailureDetail           string
	StartedAt               time.Time
	CompletedAt             *time.Time
}

// TableName pins the schema-owned model_calls table name.
func (ModelCall) TableName() string {
	return "model_calls"
}
