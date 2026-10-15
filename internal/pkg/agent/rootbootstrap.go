package agent

import (
	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
)

const rootWorkflowBootstrapVersion = 1

type workflowRootOrigin struct {
	EventID   uuid.UUID `json:"eventId"`
	EventType string    `json:"eventType"`
}

type workflowRootBootstrap struct {
	Version          int                    `json:"version"`
	SessionID        uuid.UUID              `json:"sessionId"`
	TurnID           uuid.UUID              `json:"turnId"`
	RequestID        uuid.UUID              `json:"requestId"`
	SessionCreated   bool                   `json:"sessionCreated"`
	RootAgent        string                 `json:"rootAgent"`
	Message          string                 `json:"message"`
	Workspace        string                 `json:"workspace"`
	Model            string                 `json:"model"`
	ReasoningEffort  elelem.ReasoningEffort `json:"reasoningEffort"`
	SystemPrompt     string                 `json:"systemPrompt"`
	SystemPromptMode PromptMode             `json:"systemPromptMode"`
	SourceEventID    uuid.UUID              `json:"sourceEventId"`
	Origin           *workflowRootOrigin    `json:"origin,omitempty"`
}

func (b *workflowRootBootstrap) hasSupportedVersion() bool {
	return b != nil && b.Version == rootWorkflowBootstrapVersion
}

func (r *Runtime) rootWorkflowBootstrap(
	p *preparedTurn,
) *workflowRootBootstrap {
	saved := &workflowRootBootstrap{
		Version:   rootWorkflowBootstrapVersion,
		SessionID: p.lease.SessionID, TurnID: p.lease.TurnID,
		RequestID: p.turn.requestID, SessionCreated: p.opened.Created,
		RootAgent: r.rootAgent, Message: p.input.Message,
		Workspace: p.input.Workspace, Model: p.input.Model,
		ReasoningEffort:  p.input.ReasoningEffort,
		SystemPrompt:     p.input.SystemPrompt,
		SystemPromptMode: p.input.SystemPromptMode,
		SourceEventID:    p.input.SourceEventID,
	}
	if p.rootAgent != "" {
		saved.RootAgent = p.rootAgent
	}

	if p.origin != nil {
		saved.Origin = &workflowRootOrigin{
			EventID: p.origin.EventID, EventType: p.origin.EventType,
		}
	}

	return saved
}

func (b *workflowRootBootstrap) request(sink EventSink) TurnRequest {
	sessionID := b.SessionID

	input := TurnRequest{
		SessionID: &sessionID, RequestID: b.RequestID,
		Message: b.Message, Workspace: b.Workspace, Model: b.Model,
		ReasoningEffort: b.ReasoningEffort, SystemPrompt: b.SystemPrompt,
		SystemPromptMode: b.SystemPromptMode, SourceEventID: b.SourceEventID,
		OnEvent: sink,
	}
	if b.Origin != nil {
		input.Origin = &TurnOrigin{
			EventID: b.Origin.EventID, EventType: b.Origin.EventType,
		}
	}

	return input
}
