package models

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionEvent records ordered control and activity decisions for an agent.
type ExecutionEvent struct {
	ID          uuid.UUID `json:"id"`
	SessionID   uuid.UUID `json:"sessionId"`
	ExecutionID uuid.UUID `json:"executionId"`
	Sequence    int64     `json:"sequence"`
	EventType   string    `json:"eventType"`
	PayloadJSON string    `json:"payloadJson"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (ExecutionEvent) TableName() string {
	return "execution_events"
}
