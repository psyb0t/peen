package models

import (
	"time"

	"github.com/google/uuid"
)

// Message is one durable user, assistant, or tool transcript record.
type Message struct {
	ID            uuid.UUID
	SessionID     uuid.UUID
	TurnID        uuid.UUID
	Sequence      int64
	Workspace     string
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

// TableName pins the schema-owned messages table name.
func (Message) TableName() string {
	return "messages"
}
