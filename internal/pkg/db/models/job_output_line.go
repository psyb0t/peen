package models

import (
	"time"

	"github.com/google/uuid"
)

// JobOutputLine is one ordered immutable stdout or stderr line.
type JobOutputLine struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	JobID     uuid.UUID
	Sequence  int64
	Stream    JobOutputStream
	Content   string
	CreatedAt time.Time
}

// TableName pins the schema-owned job_output_lines table name.
func (JobOutputLine) TableName() string {
	return "job_output_lines"
}
