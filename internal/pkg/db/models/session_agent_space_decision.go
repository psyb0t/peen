package models

import (
	"time"

	"github.com/google/uuid"
)

// SessionAgentSpaceDecision records one deliberate change of a session's agent
// space, with the reason the operator gave.
//
// An agent space change replaces the session's execution environment, so it is
// kept as history rather than overwritten: a later reader can tell which agent
// space ran which turns and why it moved.
type SessionAgentSpaceDecision struct {
	ID             uuid.UUID
	SessionID      uuid.UUID
	FromAgentSpace string
	ToAgentSpace   string
	Reason         string
	DecidedAt      time.Time
}

// TableName pins the schema-owned session_agent_space_decisions table name.
func (SessionAgentSpaceDecision) TableName() string {
	return "session_agent_space_decisions"
}
