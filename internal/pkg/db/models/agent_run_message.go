package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentRunMessage is one durable prompt-visible record in a child agent's own
// transcript.
//
// A child agent runs a separate model context, so its conversation is stored
// apart from the session transcript. A parent-session compaction can then
// never absorb child messages, and a child compaction can never rewrite the
// parent's.
type AgentRunMessage struct {
	ID            uuid.UUID
	SessionID     uuid.UUID
	AgentRunID    uuid.UUID
	Sequence      int64
	Role          MessageRole
	Content       string
	ModelID       string
	Thinking      string
	ToolCallsJSON string
	ToolCallID    string
	CompactionID  *uuid.UUID
	IsError       bool
	Incomplete    bool
	CreatedAt     time.Time
}

// TableName pins the schema-owned agent_run_messages table name.
func (AgentRunMessage) TableName() string {
	return "agent_run_messages"
}
