package tools

import "github.com/google/uuid"

// ForAgentRun changes process ownership without changing shared supervision.
// A zero identity selects the root conversation.
func (e *JobExecutor) ForAgentRun(agentRunID uuid.UUID) *JobExecutor {
	owner := *e
	owner.agentRunID = agentRunID

	return &owner
}
