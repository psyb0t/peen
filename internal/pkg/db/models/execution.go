package models

import (
	"time"

	"github.com/google/uuid"
)

// Execution keeps an agent's workflow identity, context, and control state.
type Execution struct {
	ID                  uuid.UUID      `json:"id"`
	SessionID           uuid.UUID      `json:"sessionId"`
	RootTurnID          uuid.UUID      `json:"rootTurnId"`
	RootExecutionID     uuid.UUID      `json:"rootExecutionId"`
	ParentExecutionID   *uuid.UUID     `json:"parentExecutionId"`
	AgentRunID          *uuid.UUID     `json:"agentRunId"`
	Kind                ExecutionKind  `json:"kind"`
	State               ExecutionState `json:"state"`
	WorkflowVersion     string         `json:"workflowVersion"`
	WorkflowExecutionID string         `json:"workflowExecutionId"`
	Task                string         `json:"task"`
	WorkingDirectory    string         `json:"workingDirectory"`
	ContextJSON         string         `json:"contextJson"`
	EventCount          int64          `json:"eventCount"`
	ControlRevision     int64          `json:"controlRevision"`
	PauseReason         string         `json:"pauseReason"`
	FailureDetail       string         `json:"failureDetail"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
	PausedAt            *time.Time     `json:"pausedAt"`
	CompletedAt         *time.Time     `json:"completedAt"`
}

func (Execution) TableName() string {
	return "executions"
}
