package models

import (
	"time"

	"github.com/google/uuid"
)

type TelegramChat struct {
	ChatID          int64 `gorm:"primaryKey"`
	SessionID       uuid.UUID
	Model           string
	ReasoningEffort string
	SystemPrompt    string
	UpdatedAt       time.Time
}

func (TelegramChat) TableName() string {
	return "telegram_chats"
}
