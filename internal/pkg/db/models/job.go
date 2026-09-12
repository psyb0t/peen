package models

import (
	"time"

	"github.com/google/uuid"
)

// Job is one durable supervised process launched from a session turn.
type Job struct {
	ID            uuid.UUID
	SessionID     uuid.UUID
	TurnID        uuid.UUID
	ToolCallID    string
	PID           int64 `gorm:"column:pid"`
	Purpose       string
	Command       string
	Directory     string
	State         JobState
	ExitCode      int64
	FailureDetail string
	StartedAt     time.Time
	EndedAt       *time.Time
}

// TableName pins the schema-owned jobs table name.
func (Job) TableName() string {
	return "jobs"
}
