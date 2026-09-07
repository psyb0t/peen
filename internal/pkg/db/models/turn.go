package models

import (
	"time"

	"github.com/google/uuid"
)

// Turn is one request execution within a session.
type Turn struct {
	ID                    uuid.UUID
	SessionID             uuid.UUID
	RequestID             uuid.UUID
	Workspace             string
	State                 TurnState
	CancelRequested       bool
	StartedAt             time.Time
	CompletedAt           *time.Time
	FailureClassification string
	ContextSnapshotHash   *string
	PromptSnapshotHash    *string
}

// TableName pins the schema-owned turns table name.
func (Turn) TableName() string {
	return "turns"
}
