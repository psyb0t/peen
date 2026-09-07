package models

import "time"

// PromptSnapshot stores one immutable effective prompt.
type PromptSnapshot struct {
	Hash            string `gorm:"primaryKey"`
	EffectivePrompt string
	CreatedAt       time.Time
}

// TableName pins the schema-owned prompt_snapshots table name.
func (PromptSnapshot) TableName() string {
	return "prompt_snapshots"
}
