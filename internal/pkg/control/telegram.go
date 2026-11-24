package control

import (
	"context"

	"github.com/google/uuid"
)

type CronNotification struct {
	RunID     uuid.UUID
	ChatID    int64
	SessionID uuid.UUID
	Text      string
}

type CronNotifier interface {
	NotifyCron(context.Context, CronNotification) error
}

type TelegramController interface {
	CronNotifier
	Run(context.Context) error
	Close()
}
