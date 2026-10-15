package models

import (
	"time"

	"github.com/google/uuid"
)

type ExecutionInboxMessage struct {
	ID                   uuid.UUID         `json:"id"`
	SessionID            uuid.UUID         `json:"sessionId"`
	ExecutionID          uuid.UUID         `json:"executionId"`
	TurnID               uuid.UUID         `json:"turnId"`
	RequestID            uuid.UUID         `json:"requestId"`
	SourceEventID        *uuid.UUID        `json:"sourceEventId"`
	StopRevision         int64             `json:"stopRevision"`
	Sequence             int64             `json:"sequence"`
	WorkingDirectory     string            `json:"workingDirectory"`
	Message              string            `json:"message"`
	PartsJSON            string            `json:"partsJson"`
	State                InboxMessageState `json:"state"`
	DeliveredActivityID  *uuid.UUID        `json:"deliveredActivityId"`
	CreatedAt            time.Time         `json:"createdAt"`
	DeliveredAt          *time.Time        `json:"deliveredAt"`
	ForwardedExecutionID *uuid.UUID        `json:"forwardedExecutionId"`
}

func (ExecutionInboxMessage) TableName() string {
	return "execution_inbox_messages"
}
