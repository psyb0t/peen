package models

import "time"

type HistoryEvent struct {
	RowID           int64 `gorm:"column:rowid"`
	ID              string
	SequenceID      int64
	InstanceID      string
	ExecutionID     string
	EventType       uint
	Timestamp       time.Time
	ScheduleEventID int64
	VisibleAt       *time.Time
}

func (HistoryEvent) TableName() string { return "history" }
