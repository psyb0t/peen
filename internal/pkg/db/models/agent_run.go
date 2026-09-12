package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentRun is one durable child-agent execution launched from a session turn.
type AgentRun struct {
	ID                    uuid.UUID
	SessionID             uuid.UUID
	ParentTurnID          uuid.UUID
	ParentAgentRunID      *uuid.UUID
	ParentToolCallID      string
	RequestID             uuid.UUID
	Name                  string
	Definition            AgentRunDefinition
	Depth                 int64
	Workspace             string
	ModelReference        string
	ModelID               string
	Task                  string
	Instructions          string
	AllowedToolsJSON      string
	SystemPrompt          string
	EventCount            int64
	CancelRequested       bool
	State                 AgentRunState
	ResponseText          string
	ResponseThinking      string
	ResponseMessagesJSON  string
	FinishReason          string
	PromptTokenCount      int64
	CompletionTokenCount  int64
	FailureClassification string
	FailureDetail         string
	StartedAt             time.Time
	EndedAt               *time.Time
}

// TableName pins the schema-owned agent_runs table name.
func (AgentRun) TableName() string {
	return "agent_runs"
}
