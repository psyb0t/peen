package models

import (
	"time"

	"github.com/google/uuid"
)

// Event is one ordered internal or protocol event in a session transcript.
type Event struct {
	ID               uuid.UUID
	SessionID        uuid.UUID
	TurnID           uuid.UUID
	Sequence         int64
	RequestID        uuid.UUID
	EventType        string
	PayloadJSON      string
	ParentToolCallID string
	CreatedAt        time.Time
}

// TableName pins the schema-owned events table name.
func (Event) TableName() string {
	return "events"
}
