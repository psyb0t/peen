package models

import (
	"time"

	"github.com/google/uuid"
)

// Session is the durable metadata for one Peen conversation.
type Session struct {
	ID                    uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
	LastMessageAt         *time.Time
	MessageCount          int64
	CompletedTurnCount    int64
	LastCompletedSequence int64
	ActiveContextHash     string
	RootAgent             string
	ModelID               string
	Workspace             string

	// ExecutionProfile is the operator-defined profile this session's tools
	// run under. It is empty for a session recorded before profiles existed,
	// which the supervisor reads as the deployment default.
	ExecutionProfile string
}

// TableName pins the schema-owned sessions table name.
func (Session) TableName() string {
	return "sessions"
}
