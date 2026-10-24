package models

import "time"

type EventPayload struct {
	ID              string
	SequenceID      int64
	EventType       uint
	Timestamp       time.Time
	ScheduleEventID int64
	VisibleAt       *time.Time
	InstanceID      string
	ExecutionID     string
	Data            []byte
}

type QueueCount struct {
	Queue string
	Count int64
}
