package session

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type ActivityDecision string

const (
	ActivityDecisionExecute   ActivityDecision = "execute"
	ActivityDecisionReuse     ActivityDecision = "reuse"
	ActivityDecisionPaused    ActivityDecision = "paused"
	ActivityDecisionReconcile ActivityDecision = "reconcile"
)

type ScheduleActivityInput struct {
	StepID       string                      `json:"stepId"`
	Kind         models.ActivityKind         `json:"kind"`
	ReplayPolicy models.ActivityReplayPolicy `json:"replayPolicy"`
	Name         string                      `json:"name"`
	Input        json.RawMessage             `json:"input"`
}

type ActivityLease struct {
	ID           uuid.UUID `json:"id"`
	GenerationID uuid.UUID `json:"generationId"`
	Attempt      int64     `json:"attempt"`
}

type ActivityClaim struct {
	Activity *models.ExecutionActivity `json:"activity"`
	Lease    ActivityLease             `json:"lease"`
	Decision ActivityDecision          `json:"decision"`
}

type CompleteActivityInput struct {
	Output          json.RawMessage           `json:"output"`
	Context         json.RawMessage           `json:"context"`
	Finish          *FinishExecutionInput     `json:"finish,omitempty"`
	Compaction      *CompactionInput          `json:"compaction,omitempty"`
	RootProjection  *ExecutionRootProjection  `json:"rootProjection,omitempty"`
	ChildProjection *ExecutionChildProjection `json:"childProjection,omitempty"`
}

type ExecutionChildProjection struct {
	ControlRevision int64                   `json:"controlRevision"`
	Messages        []AgentRunMessageInput  `json:"messages"`
	Events          []AgentRunEventInput    `json:"events"`
	Question        *ExecutionChildQuestion `json:"question,omitempty"`
}

type ExecutionChildQuestion struct {
	EventID       uuid.UUID `json:"eventId"`
	Question      string    `json:"question"`
	ParentMessage string    `json:"parentMessage"`
}

type ExecutionRootProjection struct {
	Holder   ExecutionFinishHolder `json:"holder"`
	Messages []MessageInput        `json:"messages"`
	Events   []EventInput          `json:"events"`
}

type ExecutionActivityCompletion struct {
	Activity *models.ExecutionActivity `json:"activity"`
	Events   []EventInput              `json:"events"`
}

const (
	ExecutionFinishActivityName = "finishExecutionV1"
	ExecutionFinishStepID       = "finish"
)

type FinishExecutionInput struct {
	State            models.ExecutionState  `json:"state"`
	WorkingDirectory string                 `json:"workingDirectory,omitempty"`
	FailureDetail    string                 `json:"failureDetail,omitempty"`
	PendingOutput    json.RawMessage        `json:"pendingOutput,omitempty"`
	RootHolder       *ExecutionFinishHolder `json:"rootHolder,omitempty"`
	Turn             *FinalizeTurnInput     `json:"turn,omitempty"`
	Child            *ExecutionFinishChild  `json:"child,omitempty"`
	ChildReport      string                 `json:"childReport,omitempty"`
}

type ExecutionFinishChild struct {
	State                 models.AgentRunState `json:"state"`
	ResponseText          string               `json:"responseText"`
	ResponseThinking      string               `json:"responseThinking"`
	ResponseMessagesJSON  string               `json:"responseMessagesJson"`
	FinishReason          string               `json:"finishReason"`
	PromptTokenCount      int64                `json:"promptTokenCount"`
	CompletionTokenCount  int64                `json:"completionTokenCount"`
	FailureClassification string               `json:"failureClassification"`
	FailureDetail         string               `json:"failureDetail"`
}

type ExecutionFinishHolder struct {
	LeaseID         uuid.UUID `json:"leaseId"`
	ControlRevision int64     `json:"controlRevision"`
}

type ActivityResolution string

const (
	ActivityResolutionRetry  ActivityResolution = "retry"
	ActivityResolutionAccept ActivityResolution = "accept"
)

type ResolveActivityInput struct {
	Attempt    int64              `json:"attempt"`
	Resolution ActivityResolution `json:"resolution"`
	Evidence   string             `json:"evidence"`
	Output     json.RawMessage    `json:"output,omitempty"`
	Context    json.RawMessage    `json:"context,omitempty"`
}

type ExecutionActivityPage struct {
	Items   []*models.ExecutionActivity `json:"items"`
	Limit   int                         `json:"limit"`
	Offset  int                         `json:"offset"`
	HasMore bool                        `json:"hasMore"`
}
