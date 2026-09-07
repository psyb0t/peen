package models

type (
	// TurnState is the durable lifecycle state of one session turn.
	TurnState string
	// MessageRole identifies a transcript message source.
	MessageRole string
)

const (
	TurnStateRunning     TurnState = "running"
	TurnStateCompleted   TurnState = "completed"
	TurnStateFailed      TurnState = "failed"
	TurnStateCancelled   TurnState = "cancelled"
	TurnStateInterrupted TurnState = "interrupted"

	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)
