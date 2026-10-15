package events

import "github.com/google/uuid"

const JobSource = "tools.job"

// JobNoticeData retains the process identity and its completion result.
type JobNoticeData struct {
	JobID      uuid.UUID `json:"jobId"`
	ExitCode   int       `json:"exitCode"`
	DurationMs int64     `json:"durationMs"`
	Duration   string    `json:"duration"`
}
