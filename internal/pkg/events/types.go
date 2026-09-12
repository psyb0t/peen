// Package events carries typed notices about things that happened which the
// agent did not directly ask about: a process job ended, a child agent
// finished, or something outside Peen reported a change.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Type names one kind of session event. It is a dotted, lowercase, namespaced
// name. Peen reserves the job. and agent. prefixes for its own producers;
// every other prefix belongs to the deployment, so an application is free to
// publish app.error or ci.failed and have those be first-class.
//
// It is an alias because it carries no behavior and crosses the HTTP and JSON
// boundaries as a plain string.
type Type = string

const (
	// TypeJobExited reports a process job that ended on its own.
	TypeJobExited Type = "job.exited"
	// TypeJobSignalled reports a process job terminated by a signal.
	TypeJobSignalled Type = "job.signalled"
	// TypeJobFailed reports a process job that could not run.
	TypeJobFailed Type = "job.failed"
	// TypeAgentFinished reports a child agent run that completed.
	TypeAgentFinished Type = "agent.finished"
	// TypeAgentFailed reports a child agent run that failed.
	TypeAgentFailed Type = "agent.failed"
)

// ReservedPrefixes are the type namespaces only Peen's own producers may
// publish. An outside caller posting one is rejected so it cannot forge a job
// or agent event.
//
//nolint:gochecknoglobals // A fixed lookup table, never reassigned.
var ReservedPrefixes = []string{"job.", "agent."}

// Delivery selects whether an event waits for the next turn or starts one.
type Delivery = string

const (
	// DeliveryQueue waits for the next tool boundary or turn. The default.
	DeliveryQueue Delivery = "queue"
	// DeliveryWake starts a turn when the session is idle and a handler
	// matches. It degrades to DeliveryQueue when a turn is already running or
	// no handler matches.
	DeliveryWake Delivery = "wake"
)

// Notice is one thing that happened which the session should learn about.
//
// Summary and Data are caller-supplied for externally published events, so
// both are treated as untrusted input everywhere downstream. They are quoted as
// data when they reach a model and never merged into a system prompt.
type Notice struct {
	ID        uuid.UUID       `json:"id"`
	SessionID uuid.UUID       `json:"sessionId"`
	Type      Type            `json:"type"`
	Source    string          `json:"source"`
	Summary   string          `json:"summary"`
	Data      json.RawMessage `json:"data,omitempty"`
	Delivery  Delivery        `json:"delivery"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Publisher publishes one notice with the context that produced it. The
// runtime implementation persists before it fans out to live subscribers.
type Publisher interface {
	PublishContext(context.Context, Notice) (Notice, error)
}

// Batch is what one drain returns: the events still pending plus how many were
// dropped before them, so a reader can tell that its view has a hole.
type Batch struct {
	Notices []Notice
	Dropped int
}
