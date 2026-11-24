package models

import "github.com/google/uuid"

// TelegramMessage binds a delivered notification to its durable run.
type TelegramMessage struct {
	ChatID    int64 `gorm:"primaryKey"`
	MessageID int64 `gorm:"primaryKey"`
	CronRunID *uuid.UUID
}

func (TelegramMessage) TableName() string {
	return "telegram_messages"
}
