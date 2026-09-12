package models

type (
	// TurnState is the durable lifecycle state of one session turn.
	TurnState string
	// MessageRole identifies a transcript message source.
	MessageRole string
	// AgentRunState is the durable lifecycle state of one child-agent run.
	AgentRunState string
	// AgentRunDefinition records whether a child is stored or inline.
	AgentRunDefinition string
	// NoticeDelivery identifies whether an outside notice can wake a session.
	NoticeDelivery string
	// NoticeState records whether a notice has reached the model context.
	NoticeState string
	// JobState is the durable lifecycle state of one supervised process.
	JobState string
	// JobOutputStream identifies one process output stream.
	JobOutputStream string
	// JobSignal identifies a requested process signal.
	JobSignal string
	// ModelRunStage identifies the Peen operation that issued model work.
	ModelRunStage string
	// ModelRunState is the durable lifecycle state of one logical model run.
	ModelRunState string
	// ModelCallState is the durable lifecycle state of one provider round.
	ModelCallState string
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

	AgentRunStateRunning     AgentRunState = "running"
	AgentRunStateCompleted   AgentRunState = "completed"
	AgentRunStateFailed      AgentRunState = "failed"
	AgentRunStateCancelled   AgentRunState = "cancelled"
	AgentRunStateInterrupted AgentRunState = "interrupted"

	AgentRunDefinitionStored AgentRunDefinition = "stored"
	AgentRunDefinitionAdHoc  AgentRunDefinition = "ad-hoc"

	NoticeDeliveryQueue NoticeDelivery = "queue"
	NoticeDeliveryWake  NoticeDelivery = "wake"

	NoticeStatePending   NoticeState = "pending"
	NoticeStateDelivered NoticeState = "delivered"

	JobStateRunning     JobState = "running"
	JobStateExited      JobState = "exited"
	JobStateSignalled   JobState = "signalled"
	JobStateFailed      JobState = "failed"
	JobStateInterrupted JobState = "interrupted"

	JobOutputStreamStdout JobOutputStream = "stdout"
	JobOutputStreamStderr JobOutputStream = "stderr"

	JobSignalStop JobSignal = "stop"
	JobSignalKill JobSignal = "kill"

	ModelRunStageTurn       ModelRunStage = "turn"
	ModelRunStageChild      ModelRunStage = "child"
	ModelRunStageCompaction ModelRunStage = "compaction"

	ModelRunStateRunning     ModelRunState = "running"
	ModelRunStateCompleted   ModelRunState = "completed"
	ModelRunStateFailed      ModelRunState = "failed"
	ModelRunStateCancelled   ModelRunState = "cancelled"
	ModelRunStateInterrupted ModelRunState = "interrupted"

	ModelCallStateRunning     ModelCallState = "running"
	ModelCallStateCompleted   ModelCallState = "completed"
	ModelCallStateFailed      ModelCallState = "failed"
	ModelCallStateCancelled   ModelCallState = "cancelled"
	ModelCallStateInterrupted ModelCallState = "interrupted"
)
