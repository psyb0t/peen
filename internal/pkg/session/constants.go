package session

const (
	ExecutionUserStopReason   = "user requested stop"
	ExecutionUserCancelReason = "user cancelled child agent"
	AgentMessageSenderAgent   = "agent"

	ExecutionTerminalHooksActivityName = "terminalHooksV1"
	// ExecutionToolPhaseActivityName names the activities of a model's tool
	// call. Hook steps are tool activities too, under other names.
	ExecutionToolPhaseActivityName = "toolPhaseV1"

	EventTypeUserMessageCreated   = "userMessage.created"
	EventTypeUserMessageQueued    = "userMessage.queued"
	EventTypeUserMessageDelivered = "userMessage.delivered"
	EventTypeSkillInjected        = "skill.injected"
	EventTypeAgentRunCompleted    = "agent.run.completed"
	EventTypeAgentRunFailed       = "agent.run.failed"
	EventTypeAgentRunCancelled    = "agent.run.cancelled"
	EventTypeTurnFailed           = "turn.failed"
	EventTypeTurnCancelled        = "turn.cancelled"

	eventTypeToolUse    = "tool.use"
	eventTypeToolResult = "tool.result"

	// reasonStaleCompactionHead is the log reason for a compaction left out
	// of the session chain because a later compaction replaced its head.
	reasonStaleCompactionHead = "stale_compaction_head"

	eventTypeAgentRunToolUse    = "agent.run.tool.use"
	eventTypeAgentRunToolResult = "agent.run.tool.result"

	// StoppedToolResultContent is the result Peen records for a tool call
	// that was still running when its turn or child run was stopped. The
	// turn end notice reads it as a call with no real result.
	StoppedToolResultContent = "The tool call was stopped before it " +
		"returned a result. Its effect is unknown."

	// TurnFailureAgentRun and TurnFailureCancelled are the reasons a turn's
	// terminal frame carries, the only values the WebSocket contract allows.
	TurnFailureAgentRun  = "agentRun"
	TurnFailureCancelled = "cancelled"

	// TurnEndNoticeSource is the source of the turn.failed and turn.cancelled
	// notices Peen records when a turn ends without completing.
	TurnEndNoticeSource = "session.turn"

	turnFailedNoticeSummary      = "A turn failed before it finished."
	turnInterruptedNoticeSummary = "A turn was interrupted before it finished."
	turnCancelledNoticeSummary   = "A turn was stopped before it finished."

	// turnInterruptedDetail is the error of a turn whose worker or controller
	// stopped before the turn ended.
	turnInterruptedDetail = "the worker or controller stopped mid-turn"

	// turnEndDetailBytes bounds the error a turn end frame and notice carry.
	turnEndDetailBytes = 2048
	// turnEndMessageBytes bounds each user message a turn end notice
	// carries, and turnEndUndeliveredLimit how many undelivered ones.
	turnEndMessageBytes     = 1000
	turnEndUndeliveredLimit = 10
	// turnEndToolCallLimit bounds how many of the ended turn's latest tool
	// calls the notice lists, and turnEndToolArgumentBytes each one's
	// arguments.
	turnEndToolCallLimit     = 20
	turnEndToolArgumentBytes = 300

	// resumeNoticeIdentity derives the request ID of the notice a paused
	// child reads ahead of the message that resumes it, from that message's
	// request ID, so repeating the message cannot queue the notice twice.
	resumeNoticeIdentity = "resumeNotice"

	workerStateStarting = "starting"
	workerStateReady    = "ready"
	workerStateStopped  = "stopped"
	workerStateFailed   = "failed"
)
