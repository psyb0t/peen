package models

import (
	"time"

	"github.com/google/uuid"
)

// CronJob remembers a job's durable schedule baseline and continuation.
type CronJob struct {
	JobName         string `gorm:"primaryKey"`
	SessionID       *uuid.UUID
	NextScheduledAt time.Time
	DefinitionHash  string
}

func (CronJob) TableName() string {
	return "cron_jobs"
}
