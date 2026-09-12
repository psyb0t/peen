package models

import (
	"time"

	"github.com/google/uuid"
)

// JobSignalRequest records every accepted or no-op request to signal a job.
type JobSignalRequest struct {
	ID             uuid.UUID
	SessionID      uuid.UUID
	JobID          uuid.UUID
	Signal         JobSignal
	Accepted       bool
	StateAtRequest JobState
	RequestedAt    time.Time
}

// TableName pins the schema-owned job_signal_requests table name.
func (JobSignalRequest) TableName() string {
	return "job_signal_requests"
}
