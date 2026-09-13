package models

import (
	"time"

	"github.com/google/uuid"
)

// SessionNotice is a durable report waiting for or already delivered to a
// session.
type SessionNotice struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	Sequence    int64
	Type        string
	Source      string
	Summary     string
	DataJSON    string
	Delivery    NoticeDelivery
	State       NoticeState
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// TableName pins the schema-owned session_notices table name.
func (SessionNotice) TableName() string {
	return "session_notices"
}
