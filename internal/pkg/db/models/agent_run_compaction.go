package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentRunCompaction is one immutable summary covering a completed prefix of
// one child agent's transcript.
//
// DirectFromSequence and DirectToSequence record only the messages this row
// covered itself. FromSequence and ToSequence span the whole superseded
// lineage, so a later compaction extends the chain without rewriting the
// direct membership an earlier row already recorded.
type AgentRunCompaction struct {
	ID                     uuid.UUID
	SessionID              uuid.UUID
	AgentRunID             uuid.UUID
	FromMessageID          uuid.UUID
	ToMessageID            uuid.UUID
	FromSequence           int64
	ToSequence             int64
	DirectFromSequence     int64
	DirectToSequence       int64
	Summary                string
	SourceMessageCount     int64
	InputTokenCount        int64
	SummaryTokenCount      int64
	ModelID                string
	PromptHash             string
	CreatedAt              time.Time
	SupersedesCompactionID *uuid.UUID
}

// TableName pins the schema-owned agent_run_compactions table name.
func (AgentRunCompaction) TableName() string {
	return "agent_run_compactions"
}
