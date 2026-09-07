package models

import (
	"time"

	"github.com/google/uuid"
)

// Compaction is one immutable summary covering a completed message prefix.
type Compaction struct {
	ID                     uuid.UUID
	SessionID              uuid.UUID
	FromMessageID          uuid.UUID
	ToMessageID            uuid.UUID
	FromSequence           int64
	ToSequence             int64
	Summary                string
	SourceMessageCount     int64
	InputTokenCount        int64
	SummaryTokenCount      int64
	ModelID                string
	PromptHash             string
	CreatedAt              time.Time
	SupersedesCompactionID *uuid.UUID
}

// TableName pins the schema-owned compactions table name.
func (Compaction) TableName() string {
	return "compactions"
}
