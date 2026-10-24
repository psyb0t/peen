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
	EventTypeTurnFailed           = "turn.failed"
	EventTypeTurnCancelled        = "turn.cancelled"

	// TurnFailureAgentRun and TurnFailureCancelled are the reasons a turn's
	// terminal frame carries, the only values the WebSocket contract allows.
	TurnFailureAgentRun  = "agentRun"
	TurnFailureCancelled = "cancelled"

	workerStateStarting = "starting"
	workerStateReady    = "ready"
	workerStateStopped  = "stopped"
	workerStateFailed   = "failed"
)
