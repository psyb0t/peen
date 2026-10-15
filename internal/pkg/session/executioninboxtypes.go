package session

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type ExecutionInboxPart struct {
	Content     string `json:"content"`
	SkillName   string `json:"skillName"`
	SkillIsNote bool   `json:"skillIsNote"`
}

type AcceptExecutionMessageInput struct {
	ExpectedStopRevision *int64 `json:"expectedStopRevision,omitempty"`

	RequestID        uuid.UUID            `json:"requestId"`
	SourceEventID    uuid.UUID            `json:"sourceEventId,omitempty"`
	WorkingDirectory string               `json:"workingDirectory"`
	Message          string               `json:"message"`
	Parts            []ExecutionInboxPart `json:"parts"`
	Capacity         int                  `json:"capacity"`
}

type ExecutionInboxPage struct {
	Items   []*models.ExecutionInboxMessage `json:"items"`
	Limit   int                             `json:"limit"`
	Offset  int                             `json:"offset"`
	HasMore bool                            `json:"hasMore"`
}

type ExecutionInboxAcceptance struct {
	Message *models.ExecutionInboxMessage `json:"message"`
	Events  []EventInput                  `json:"events"`
}

type ExecutionInboxDelivery struct {
	Messages []MessageInput `json:"messages"`
	Events   []EventInput   `json:"events"`
}

type ListExecutionInboxOptions struct {
	State  models.InboxMessageState `json:"state,omitempty"`
	Limit  int                      `json:"limit"`
	Offset int                      `json:"offset"`
}

type DeliverExecutionMessagesInput struct {
	TurnLease  Lease         `json:"turnLease"`
	ModelLease ActivityLease `json:"modelLease"`
	RequestIDs []uuid.UUID   `json:"requestIds"`
}

type executionInboxMessagePayload struct {
	Message       string `json:"message"`
	SourceEventID string `json:"sourceEventId,omitempty"`
}
