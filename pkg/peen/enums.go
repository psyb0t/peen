package peen

import "github.com/psyb0t/peen/internal/pkg/http/api"

// ModelRunStage identifies root, child or summarization provider work.
type ModelRunStage string

const (
	ModelRunStageTurn ModelRunStage = ModelRunStage(
		api.ModelRunStageTurn,
	)
	ModelRunStageChild ModelRunStage = ModelRunStage(
		api.ModelRunStageChild,
	)
	ModelRunStageCompaction ModelRunStage = ModelRunStage(
		api.ModelRunStageCompaction,
	)
)

// AgentRunDefinition identifies a finite value in durable replay or control.
type AgentRunDefinition string

const (
	AgentRunDefinitionAdHoc AgentRunDefinition = AgentRunDefinition(
		api.AgentRunDefinitionAdHoc,
	)
	AgentRunDefinitionStored AgentRunDefinition = AgentRunDefinition(
		api.AgentRunDefinitionStored,
	)
)

// AgentRunState identifies a finite value in durable replay or control.
type AgentRunState string

// AgentRunStateWaiting filters waiting runs, whose stored state is running.
const AgentRunStateWaiting AgentRunState = AgentRunState(
	api.ListSessionAgentRunsParamsStateWaiting,
)

const (
	AgentRunStateCancelled AgentRunState = AgentRunState(
		api.AgentRunStateCancelled,
	)
	AgentRunStateCompleted AgentRunState = AgentRunState(
		api.AgentRunStateCompleted,
	)
	AgentRunStateFailed AgentRunState = AgentRunState(
		api.AgentRunStateFailed,
	)
	AgentRunStateInterrupted AgentRunState = AgentRunState(
		api.AgentRunStateInterrupted,
	)
	AgentRunStateRunning AgentRunState = AgentRunState(
		api.AgentRunStateRunning,
	)
)

// JobState identifies a finite value in durable replay or control.
type JobState string

const (
	JobStateExited JobState = JobState(
		api.JobStateExited,
	)
	JobStateFailed JobState = JobState(
		api.JobStateFailed,
	)
	JobStateInterrupted JobState = JobState(
		api.JobStateInterrupted,
	)
	JobStateRunning JobState = JobState(
		api.JobStateRunning,
	)
	JobStateSignalled JobState = JobState(
		api.JobStateSignalled,
	)
)

// JobStream identifies a finite value in durable replay or control.
type JobStream string

const (
	JobStreamStderr JobStream = JobStream(
		api.JobOutputLineStreamStderr,
	)
	JobStreamStdout JobStream = JobStream(
		api.JobOutputLineStreamStdout,
	)
)

// JobSignal identifies a finite value in durable replay or control.
type JobSignal string

const (
	JobSignalKill JobSignal = JobSignal(
		api.JobSignalRequestSignalKill,
	)
	JobSignalStop JobSignal = JobSignal(
		api.JobSignalRequestSignalStop,
	)
)

// ModelCallState identifies a finite value in durable replay or control.
type ModelCallState string

const (
	ModelCallStateCancelled ModelCallState = ModelCallState(
		api.ModelCallStateCancelled,
	)
	ModelCallStateCompleted ModelCallState = ModelCallState(
		api.ModelCallStateCompleted,
	)
	ModelCallStateFailed ModelCallState = ModelCallState(
		api.ModelCallStateFailed,
	)
	ModelCallStateInterrupted ModelCallState = ModelCallState(
		api.ModelCallStateInterrupted,
	)
	ModelCallStateRunning ModelCallState = ModelCallState(
		api.ModelCallStateRunning,
	)
)

// ModelRunState identifies a finite value in durable replay or control.
type ModelRunState string

const (
	ModelRunStateCancelled ModelRunState = ModelRunState(
		api.ModelRunStateCancelled,
	)
	ModelRunStateCompleted ModelRunState = ModelRunState(
		api.ModelRunStateCompleted,
	)
	ModelRunStateFailed ModelRunState = ModelRunState(
		api.ModelRunStateFailed,
	)
	ModelRunStateInterrupted ModelRunState = ModelRunState(
		api.ModelRunStateInterrupted,
	)
	ModelRunStateRunning ModelRunState = ModelRunState(
		api.ModelRunStateRunning,
	)
)

// Delivery identifies a finite value in durable replay or control.
type Delivery string

const (
	DeliveryQueue Delivery = Delivery(
		api.SessionNoticeDeliveryQueue,
	)
	DeliveryWake Delivery = Delivery(
		api.SessionNoticeDeliveryWake,
	)
)

// TurnState identifies a finite value in durable replay or control.
type TurnState string

const (
	TurnStateCancelled TurnState = TurnState(
		api.TurnStateCancelled,
	)
	TurnStateCompleted TurnState = TurnState(
		api.TurnStateCompleted,
	)
	TurnStateFailed TurnState = TurnState(
		api.TurnStateFailed,
	)
	TurnStateInterrupted TurnState = TurnState(
		api.TurnStateInterrupted,
	)
	TurnStateRunning TurnState = TurnState(
		api.TurnStateRunning,
	)
)
