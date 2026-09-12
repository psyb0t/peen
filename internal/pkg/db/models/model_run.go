package models

import (
	"time"

	"github.com/google/uuid"
)

// ModelRun records one logical Elelem Run invocation. It keeps the complete
// non-secret input and output audit record while ModelCall splits its provider
// rounds into separately replayable rows.
type ModelRun struct {
	ID                     uuid.UUID
	SessionID              uuid.UUID
	TurnID                 uuid.UUID
	AgentRunID             *uuid.UUID
	Stage                  ModelRunStage
	ModelReference         string
	ConnectionName         string
	RequestedModelID       string
	ResponseModelID        string
	RequestSettingsJSON    string
	State                  ModelRunState
	ResponseText           string
	ResponseThinking       string
	ResponseMessagesJSON   string
	ResponseInjectionsJSON string
	ResponseUsageJSON      string
	ResponseCostAmount     string
	RetryCostAmount        string
	BilledCostAmount       string
	CostKnown              bool
	FinishReason           string
	FailureClassification  string
	FailureDetail          string
	StartedAt              time.Time
	CompletedAt            *time.Time
}

// TableName pins the schema-owned model_runs table name.
func (ModelRun) TableName() string {
	return "model_runs"
}
