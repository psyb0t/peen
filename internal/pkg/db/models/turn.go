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

	// WorkerGenerationID names the worker process that ran this turn. It is
	// empty for a turn recorded before worker generations existed.
	WorkerGenerationID string
}

// TableName pins the schema-owned turns table name.
func (Turn) TableName() string {
	return "turns"
}
