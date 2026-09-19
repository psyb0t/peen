package models

import (
	"time"

	"github.com/google/uuid"
)

// SessionProfileDecision records one deliberate change of a session's
// execution profile, with the reason the operator gave.
//
// A profile change replaces the session's execution environment, so it is kept
// as history rather than overwritten: a later reader can tell which profile ran
// which turns and why it moved.
type SessionProfileDecision struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	FromProfile string
	ToProfile   string
	Reason      string
	DecidedAt   time.Time
}

// TableName pins the schema-owned session_profile_decisions table name.
func (SessionProfileDecision) TableName() string {
	return "session_profile_decisions"
}
