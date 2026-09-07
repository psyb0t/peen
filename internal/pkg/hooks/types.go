// Package hooks runs validated layered hook actions at agent lifecycle points.
package hooks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const (
	defaultCommandTimeout  = 30 * time.Second
	defaultCommandOutput   = 64 * 1024
	commandDecisionAllow   = "allow"
	commandDecisionDeny    = "deny"
	hookFailureEventType   = "hook.action_failed"
	hookFailureEventSource = "hooks"
)

// EventPublisher is the bounded session event dependency used by emit_event.
type EventPublisher interface {
	Publish(events.Notice) (events.Notice, error)
}

// CommandInput is the fully structured event a command action receives on
// stdin. Arguments are passed directly without a shell.
type CommandInput struct {
	Command     string
	Args        []string
	Environment map[string]string
	WorkingDir  string
	Stdin       []byte
	MaxOutput   int
}

// CommandRunner is a test seam around a direct executable invocation.
type CommandRunner func(context.Context, CommandInput) ([]byte, error)

// Options defines the execution policy for one turn's resolved hook list.
type Options struct {
	Snapshot             harness.Snapshot
	Workspace            string
	EnableWorkspaceHooks bool
	CommandTimeout       time.Duration
	MaxCommandOutput     int
	Publisher            EventPublisher
	RunCommand           CommandRunner
}

// Invocation is one full lifecycle occurrence available to hook matching and
// command stdin. Paths are canonical host paths chosen by the tool adapter.
type Invocation struct {
	Event     harness.HookEvent `json:"event"`
	SessionID uuid.UUID         `json:"sessionId,omitempty"`
	RequestID uuid.UUID         `json:"requestId,omitempty"`
	TurnID    uuid.UUID         `json:"turnId,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	CallID    string            `json:"callId,omitempty"`
	Workspace string            `json:"workspace"`
	Paths     []string          `json:"paths,omitempty"`
	Input     json.RawMessage   `json:"input,omitempty"`
	Result    json.RawMessage   `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// Outcome is the non-destructive effect of a hook invocation. A caller makes
// injected messages model-visible at its next safe protocol boundary.
type Outcome struct {
	Injections []string
}

type commandDecision struct {
	Decision string         `json:"decision"`
	Reason   string         `json:"reason"`
	Message  string         `json:"message"`
	Events   []emittedEvent `json:"events"`
}

type emittedEvent struct {
	Type     string          `json:"type"`
	Summary  string          `json:"summary"`
	Data     json.RawMessage `json:"data"`
	Delivery string          `json:"delivery"`
}
