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
}

// TableName pins the schema-owned sessions table name.
func (Session) TableName() string {
	return "sessions"
}
