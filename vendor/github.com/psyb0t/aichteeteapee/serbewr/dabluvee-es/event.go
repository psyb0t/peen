package dabluveees

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
)

type EventType string

const (
	EventTypeSystemLog   EventType = "system.log"
	EventTypeShellExec   EventType = "shell.exec"
	EventTypeEchoRequest EventType = "echo.request"
	EventTypeEchoReply   EventType = "echo.reply"
	EventTypeError       EventType = "error"
)

type Event struct {
	ID   uuid.UUID       `json:"id"` // UUID4 identifier
	Type EventType       `json:"type"`
	Data json.RawMessage `json:"data"`
	// Unix timestamp (seconds) - SET BY SENDER
	Timestamp int64 `json:"timestamp"`
	// For rooms, userID, etc.
	Metadata *EventMetadataMap `json:"metadata"`
	// ID of triggering event
	TriggeredBy *uuid.UUID `json:"triggeredBy"`
}

// NewEvent creates a new event with current unix timestamp
// Use this when the SERVER is creating/sending an event.
func NewEvent(eventType EventType, data any) *Event {
	eventID := uuid.New()

	slog.Debug(
		"creating new event",
		aichteeteapee.FieldEventID, eventID,
		aichteeteapee.FieldEventType, string(eventType),
	)

	var rawData json.RawMessage
	if data != nil {
		if jsonData, err := json.Marshal(data); err != nil {
			slog.Error(
				"failed to marshal event data, using nil",
				"error", err,
				aichteeteapee.FieldEventID, eventID,
				aichteeteapee.FieldEventType, string(eventType),
			)
		} else {
			rawData = jsonData
		}
	}

	return &Event{
		ID:   eventID,
		Type: eventType,
		Data: rawData,
		// Server sets timestamp when server sends
		Timestamp:   time.Now().Unix(),
		Metadata:    newEventMetadataMap(),
		TriggeredBy: nil, // Not triggered by another event by default
	}
}

// SetMetadata adds metadata to an event (chainable).
func (e Event) SetMetadata(key string, value any) Event {
	if e.Metadata == nil {
		e.Metadata = newEventMetadataMap()
	}

	e.Metadata.Set(key, value)

	return e
}

// SetTimestamp sets a specific unix timestamp (chainable).
func (e Event) SetTimestamp(unixTimestamp int64) Event {
	e.Timestamp = unixTimestamp

	return e
}

// SetTriggeredBy sets the ID of the triggering event (chainable).
func (e Event) SetTriggeredBy(triggerEventID uuid.UUID) Event {
	e.TriggeredBy = &triggerEventID

	return e
}

// GetTime converts unix timestamp to time.Time for Go usage.
func (e Event) GetTime() time.Time {
	return time.Unix(e.Timestamp, 0)
}

// IsRecent checks if event is within the last N seconds.
func (e Event) IsRecent(seconds int64) bool {
	return time.Now().Unix()-e.Timestamp <= seconds
}
