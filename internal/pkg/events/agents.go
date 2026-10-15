package events

import "github.com/google/uuid"

const (
	AgentSource                     = "agent.launchAgent"
	TypeAgentRunQuestion       Type = "agent.run.question"
	AgentQuestionSummaryFormat      = "agent %q run %s asked a question"
)

type AgentNoticeData struct {
	RunID     uuid.UUID `json:"runId"`
	AgentName string    `json:"agentName"`
	Message   string    `json:"message"`
}

type AgentQuestionPayload struct {
	Question string `json:"question"`
	CallID   string `json:"callId,omitempty"`
}
