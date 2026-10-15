package models

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionTurnLease retains one root turn's current workflow holder.
type ExecutionTurnLease struct {
	ExecutionID        uuid.UUID  `gorm:"primaryKey"         json:"executionId"`
	SessionID          uuid.UUID  `json:"sessionId"`
	TurnID             uuid.UUID  `json:"turnId"`
	LeaseID            uuid.UUID  `json:"leaseId"`
	OwnerGenerationID  uuid.UUID  `json:"ownerGenerationId"`
	ControlRevision    int64      `json:"controlRevision"`
	WorkerGenerationID *uuid.UUID `json:"workerGenerationId"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

func (ExecutionTurnLease) TableName() string {
	return "execution_turn_leases"
}
