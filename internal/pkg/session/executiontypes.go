package session

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type CreateExecutionInput struct {
	DeferredInboxID     *uuid.UUID      `json:"deferredInboxId,omitempty"`
	ID                  uuid.UUID       `json:"id"`
	RootTurnID          uuid.UUID       `json:"rootTurnId"`
	ParentExecutionID   *uuid.UUID      `json:"parentExecutionId,omitempty"`
	AgentRunID          *uuid.UUID      `json:"agentRunId,omitempty"`
	WorkflowVersion     string          `json:"workflowVersion"`
	WorkflowExecutionID string          `json:"workflowExecutionId,omitempty"`
	Task                string          `json:"task"`
	WorkingDirectory    string          `json:"workingDirectory"`
	Context             json.RawMessage `json:"context"`
}

type RestoreExecutionTurnInput struct {
	ControlRevision    int64     `json:"controlRevision"`
	OwnerGenerationID  uuid.UUID `json:"ownerGenerationId"`
	WorkerGenerationID uuid.UUID `json:"workerGenerationId,omitempty"`
	PreviousLeaseID    uuid.UUID `json:"previousLeaseId,omitempty"`
}

type SearchExecutionsOptions struct {
	ID       uuid.UUID             `json:"id,omitempty"`
	ParentID uuid.UUID             `json:"parentId,omitempty"`
	RootID   uuid.UUID             `json:"rootId,omitempty"`
	State    models.ExecutionState `json:"state,omitempty"`
	Text     string                `json:"text,omitempty"`
	FromTime *time.Time            `json:"fromTime,omitempty"`
	ToTime   *time.Time            `json:"toTime,omitempty"`
	Limit    int                   `json:"limit"`
	Offset   int                   `json:"offset"`
}

type ExecutionPage struct {
	Items   []*models.Execution `json:"items"`
	Limit   int                 `json:"limit"`
	Offset  int                 `json:"offset"`
	HasMore bool                `json:"hasMore"`
}

type ExecutionEventPage struct {
	Items   []*models.ExecutionEvent `json:"items"`
	Limit   int                      `json:"limit"`
	Offset  int                      `json:"offset"`
	HasMore bool                     `json:"hasMore"`
}

type ExecutionEventType string

const (
	ExecutionEventTypeCreated        ExecutionEventType = "execution.created"
	ExecutionEventTypePaused         ExecutionEventType = "execution.paused"
	ExecutionEventTypeResumed        ExecutionEventType = "execution.resumed"
	ExecutionEventTypeStarted        ExecutionEventType = "execution.started"
	ExecutionEventTypeCheckpoint     ExecutionEventType = "execution.checkpoint"
	ExecutionEventTypeCompleted      ExecutionEventType = "execution.completed"
	ExecutionEventTypeFailed         ExecutionEventType = "execution.failed"
	ExecutionEventTypeCancelled      ExecutionEventType = "execution.cancelled"
	ExecutionEventTypeStepCancelled  ExecutionEventType = "activity.cancelled"
	ExecutionEventTypeStepScheduled  ExecutionEventType = "activity.scheduled"
	ExecutionEventTypeStepStarted    ExecutionEventType = "activity.started"
	ExecutionEventTypeStepCompleted  ExecutionEventType = "activity.completed"
	ExecutionEventTypeStepFailed     ExecutionEventType = "activity.failed"
	ExecutionEventTypeStepPaused     ExecutionEventType = "activity.paused"
	ExecutionEventTypeStepUncertain  ExecutionEventType = "activity.uncertain"
	ExecutionEventTypeStepResolved   ExecutionEventType = "activity.resolved"
	ExecutionEventTypeInboxQueued    ExecutionEventType = "inbox.queued"
	ExecutionEventTypeInboxDelivered ExecutionEventType = "inbox.delivered"
	ExecutionEventTypeInboxForwarded ExecutionEventType = "inbox.forwarded"
)

const ExecutionEventTypeTurnRestored = ExecutionEventType(
	"execution.turnRestored",
)

const ExecutionEventTypeCompletionSealed = ExecutionEventType(
	"execution.completionSealed",
)

const ExecutionEventTypeInboxResumed = ExecutionEventType("inbox.resumed")
