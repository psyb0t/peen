package models

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionActivity records a stable step before and after its external work.
type ExecutionActivity struct {
	ID                uuid.UUID            `json:"id"`
	SessionID         uuid.UUID            `json:"sessionId"`
	ExecutionID       uuid.UUID            `json:"executionId"`
	StepID            string               `json:"stepId"`
	Kind              ActivityKind         `json:"kind"`
	State             ActivityState        `json:"state"`
	ReplayPolicy      ActivityReplayPolicy `json:"replayPolicy"`
	Name              string               `json:"name"`
	InputJSON         string               `json:"inputJson"`
	OutputJSON        *string              `json:"outputJson"`
	ContextJSON       string               `json:"contextJson"`
	Attempt           int64                `json:"attempt"`
	OwnerGenerationID string               `json:"ownerGenerationId"`
	FailureDetail     string               `json:"failureDetail"`
	ResolutionJSON    string               `json:"resolutionJson"`
	CreatedAt         time.Time            `json:"createdAt"`
	StartedAt         *time.Time           `json:"startedAt"`
	CompletedAt       *time.Time           `json:"completedAt"`
}

func (ExecutionActivity) TableName() string {
	return "execution_activities"
}
