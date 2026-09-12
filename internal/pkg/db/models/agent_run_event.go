package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentRunEvent is one ordered, replayable child-agent event.
type AgentRunEvent struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	AgentRunID  uuid.UUID
	Sequence    int64
	EventType   string
	PayloadJSON string
	CreatedAt   time.Time
}

// TableName pins the schema-owned agent_run_events table name.
func (AgentRunEvent) TableName() string {
	return "agent_run_events"
}
