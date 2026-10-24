package models

import "time"

type Instance struct {
	ID                    string
	ExecutionID           string
	ParentInstanceID      *string
	ParentExecutionID     *string
	ParentScheduleEventID *int64
	Metadata              []byte
	State                 int
	Queue                 string
	CreatedAt             time.Time
	CompletedAt           *time.Time
	LockedUntil           *time.Time
	StickyUntil           *time.Time
	Worker                *string
}

func (Instance) TableName() string { return "instances" }
