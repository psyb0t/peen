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

	// TurnEndNoticeSource is the source of the turn.failed and turn.cancelled
	// notices Peen records when a turn ends without completing.
	TurnEndNoticeSource = "session.turn"

	turnFailedNoticeSummary      = "The previous turn failed."
	turnInterruptedNoticeSummary = "The previous turn was interrupted."
	turnCancelledNoticeSummary   = "The previous turn was stopped."

	// turnInterruptedDetail is the error of a turn whose worker or controller
	// stopped before the turn ended.
	turnInterruptedDetail = "the worker or controller stopped mid-turn"

	// turnEndDetailBytes bounds the error a turn end frame and notice carry.
	turnEndDetailBytes = 2048

	// resumeNoticeIdentity derives the request ID of the notice a paused
	// child reads ahead of the message that resumes it, from that message's
	// request ID, so repeating the message cannot queue the notice twice.
	resumeNoticeIdentity = "resumeNotice"

	workerStateStarting = "starting"
	workerStateReady    = "ready"
	workerStateStopped  = "stopped"
	workerStateFailed   = "failed"
)
