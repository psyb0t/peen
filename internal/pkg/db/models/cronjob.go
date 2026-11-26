package models

import "time"

// CronJob remembers a job's durable schedule baseline.
type CronJob struct {
	JobName         string `gorm:"primaryKey"`
	NextScheduledAt time.Time
	DefinitionHash  string
}

func (CronJob) TableName() string {
	return "cron_jobs"
}
