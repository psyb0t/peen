package models

type (
	ExecutionKind        string
	ExecutionState       string
	ActivityKind         string
	ActivityState        string
	ActivityReplayPolicy string
	InboxMessageState    string
	// TurnState is the durable lifecycle state of one session turn.
	TurnState string
	// MessageRole identifies a transcript message source.
	MessageRole string
	// InjectedKind says what an injected message is, so a client can show it
	// as more than an unlabelled update. It is empty for a message that is
	// injected without a more specific kind.
	InjectedKind string
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
	InboxMessageStateQueued    InboxMessageState = "queued"
	InboxMessageStateDelivered InboxMessageState = "delivered"
	InboxMessageStateDeferred  InboxMessageState = "deferred"
	InboxMessageStateForwarded InboxMessageState = "forwarded"

	ExecutionKindRoot  ExecutionKind = "root"
	ExecutionKindChild ExecutionKind = "child"

	ExecutionStateQueued      ExecutionState = "queued"
	ExecutionStateRunning     ExecutionState = "running"
	ExecutionStatePaused      ExecutionState = "paused"
	ExecutionStateReconciling ExecutionState = "reconciling"
	ExecutionStateCompleted   ExecutionState = "completed"
	ExecutionStateFailed      ExecutionState = "failed"
	ExecutionStateCancelled   ExecutionState = "cancelled"

	ActivityKindModel       ActivityKind = "model"
	ActivityKindTool        ActivityKind = "tool"
	ActivityKindCompaction  ActivityKind = "compaction"
	ActivityKindPreparation ActivityKind = "preparation"

	ActivityStateScheduled ActivityState = "scheduled"
	ActivityStateRunning   ActivityState = "running"
	ActivityStateCompleted ActivityState = "completed"
	ActivityStateFailed    ActivityState = "failed"
	ActivityStatePaused    ActivityState = "paused"
	ActivityStateUncertain ActivityState = "uncertain"
	ActivityStateCancelled ActivityState = "cancelled"

	ActivityReplayPolicyRetry     ActivityReplayPolicy = "retry"
	ActivityReplayPolicyReconcile ActivityReplayPolicy = "reconcile"

	TurnStateRunning     TurnState = "running"
	TurnStateCompleted   TurnState = "completed"
	TurnStateFailed      TurnState = "failed"
	TurnStateCancelled   TurnState = "cancelled"
	TurnStateInterrupted TurnState = "interrupted"

	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"

	InjectedKindNone  InjectedKind = ""
	InjectedKindSkill InjectedKind = "skill"

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
