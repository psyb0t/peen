package models

import "time"

// ContextSnapshot stores one immutable resolved harness context.
type ContextSnapshot struct {
	Hash            string `gorm:"primaryKey"`
	ManifestJSON    string
	ResolvedContent string
	CreatedAt       time.Time
}

// TableName pins the schema-owned context_snapshots table name.
func (ContextSnapshot) TableName() string {
	return "context_snapshots"
}
