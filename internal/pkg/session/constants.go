package session

const (
	ExecutionUserStopReason   = "user requested stop"
	ExecutionUserCancelReason = "user cancelled child agent"
	AgentMessageSenderAgent   = "agent"

	ExecutionTerminalHooksActivityName = "terminalHooksV1"

	EventTypeUserMessageCreated   = "userMessage.created"
	EventTypeUserMessageQueued    = "userMessage.queued"
	EventTypeUserMessageDelivered = "userMessage.delivered"
	EventTypeSkillInjected        = "skill.injected"
	EventTypeAgentRunCompleted    = "agent.run.completed"
	EventTypeAgentRunFailed       = "agent.run.failed"
	EventTypeAgentRunCancelled    = "agent.run.cancelled"

	workerStateStarting = "starting"
	workerStateReady    = "ready"
	workerStateStopped  = "stopped"
	workerStateFailed   = "failed"
)
