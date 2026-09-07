package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

const (
	// DefaultPageLimit is the API default for transcript pagination.
	DefaultPageLimit = 50
	// MaximumPageLimit bounds one transcript page at the storage boundary.
	MaximumPageLimit = 200
)

// Options supplies deterministic seams for the transport-neutral store.
type Options struct {
	Clock func() time.Time
	NewID func() uuid.UUID

	// MaxStoredMessageBytes bounds one message row of any role. An assistant
	// or tool message has no caller-facing size limit, so without this a
	// single tool result could grow the transcript without bound. Zero takes
	// the package default.
	MaxStoredMessageBytes int
}

// OpenSessionOptions supplies immutable session attribution on creation.
type OpenSessionOptions struct {
	RootAgent string
	ModelID   string
}

// OpenSessionResult is a resolved session and whether this call created it.
type OpenSessionResult struct {
	Session *models.Session
	Created bool
}

// StartTurnInput describes the durable beginning of an agent turn.
type StartTurnInput struct {
	RequestID uuid.UUID
	Workspace string
	Messages  []MessageInput
	Events    []EventInput
}

// Lease uniquely identifies one active turn in a session.
type Lease struct {
	SessionID uuid.UUID
	TurnID    uuid.UUID
}

// MessageInput describes a durable transcript record.
type MessageInput struct {
	ID            uuid.UUID
	Workspace     string
	Role          models.MessageRole
	Content       string
	ModelID       string
	Thinking      string
	ToolCallsJSON string
	ToolCallID    string
	IsError       bool
	Incomplete    bool
}

// EventInput describes one exact internal or protocol event.
type EventInput struct {
	ID               uuid.UUID
	RequestID        uuid.UUID
	EventType        string
	PayloadJSON      string
	ParentToolCallID string
}

// FinalizeTurnInput describes terminal turn state and durable references.
type FinalizeTurnInput struct {
	State                 models.TurnState
	FailureClassification string
	ContextSnapshotHash   *string
	PromptSnapshotHash    *string
	Messages              []MessageInput
	Events                []EventInput
}

// PageOrder controls transcript pagination direction.
type PageOrder string

const (
	PageOrderAscending  PageOrder = "asc"
	PageOrderDescending PageOrder = "desc"
)

// ListMessagesOptions controls a bounded session-local message page.
type ListMessagesOptions struct {
	Limit  int
	Offset int
	Order  PageOrder
}

// MessagePage is one complete limit-offset transcript page.
type MessagePage struct {
	Items   []*models.Message
	Limit   int
	Offset  int
	HasMore bool
}

// CompactionInput stores one immutable replacement summary.
type CompactionInput struct {
	ID                     uuid.UUID
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
	SupersedesCompactionID *uuid.UUID
}

// History is the active compaction followed by its uncovered completed tail.
type History struct {
	Compaction *models.Compaction
	Messages   []*models.Message
}

type activeTurn struct {
	turnID          uuid.UUID
	cancel          context.CancelFunc
	cancelRequested bool
}
