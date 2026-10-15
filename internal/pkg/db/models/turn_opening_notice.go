package models

import "github.com/google/uuid"

// TurnOpeningNotice retains the ordered notice snapshot accepted with a turn.
type TurnOpeningNotice struct {
	SessionID      uuid.UUID
	TurnID         uuid.UUID
	NoticeID       uuid.UUID
	SourceSequence int64
}

func (TurnOpeningNotice) TableName() string {
	return "turn_opening_notices"
}
