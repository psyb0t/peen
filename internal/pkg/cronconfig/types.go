package cronconfig

import (
	"encoding/json"
	"time"

	"github.com/robfig/cron/v3"
)

// Options bounds cron jobs against the controller's configured limits.
type Options struct {
	MaxMessageBytes int
	MaxSchemaBytes  int
	TelegramEnabled bool
	ModelIDs        []string
}

// Job is one validated cron job ready for the controller scheduler.
type Job struct {
	Name            string
	ScheduleText    string
	Schedule        cron.Schedule
	TimeZone        string
	Workspace       string
	AgentSpace      string
	Message         string
	Model           string
	ReasoningEffort string
	Timeout         time.Duration
	OutputSchema    json.RawMessage
	TelegramChatID  int64
}
