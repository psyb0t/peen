package models

import (
	"time"

	"github.com/google/uuid"
)

// CronRun records one claimed schedule occurrence. ID also identifies its turn
// request, so interrupted controller work can reconcile the exact durable turn.
type CronRun struct {
	ID          uuid.UUID
	JobName     string
	ScheduledAt time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	SessionID   *uuid.UUID
	State       CronRunState
	Error       string
}

func (CronRun) TableName() string {
	return "cron_runs"
}
