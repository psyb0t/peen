package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	agentWorkflowName = "agentExecution"

	modelActivityName    = "modelRoundV1"
	prepareActivityName  = "prepareRoundV1"
	toolActivityName     = "toolCallV1"
	workflowResumeSignal = "resumeExecution"
	// Elelem reserves the last slot before returning a deferred tool executor.
	// The workflow consumes only the first provider call and owns continuation.
	workflowManualRequestRoundLimit = 2
)

type workflowActivityLeaseContextKey struct{}

type workflowExecutionInput struct {
	SessionID          uuid.UUID       `json:"sessionId"`
	ExecutionID        uuid.UUID       `json:"executionId"`
	MaxRounds          int             `json:"maxRounds"`
	MaxConcurrentTools int             `json:"maxConcurrentTools"`
	Context            json.RawMessage `json:"context"`
}

type workflowRoundInput struct {
	Execution workflowExecutionInput `json:"execution"`
	Round     int                    `json:"round"`
}

type workflowRoundResult struct {
	Context  json.RawMessage   `json:"context"`
	Calls    []elelem.ToolCall `json:"calls"`
	Text     string            `json:"text"`
	Continue bool              `json:"continue,omitempty"`
	// FinishReason is why the model stopped this round's reply. Results saved
	// before it existed read empty.
	FinishReason elelem.FinishReason `json:"finishReason,omitempty"`

	ChildProjection *childPhaseBuffer   `json:"childProjection,omitempty"`
	RootProjection  *rootToolProjection `json:"rootProjection,omitempty"`
}

type workflowToolInput struct {
	Round workflowRoundInput `json:"round"`
	Call  elelem.ToolCall    `json:"call"`
	Index int                `json:"index"`
}

type workflowToolResult struct {
	Result     elelem.ToolResult         `json:"result"`
	Injections []elelem.MessageInjection `json:"injections"`
	Failure    string                    `json:"failure,omitempty"`
	Context    json.RawMessage           `json:"context,omitempty"`
}

type workflowActivityResult[T any] struct {
	Decision session.ActivityDecision `json:"decision"`
	Output   T                        `json:"output"`
}

// Phase callbacks run in individual native activities with saved inputs.
// MergeRound only combines recorded values and must have no external effects.
type workflowExecutionOptions struct {
	OpeningHook func(context.Context, workflowOpeningInput) (
		workflowOpeningResult, error)
	AfterOpeningHook func(context.Context, workflowOpeningInput,
		workflowOpeningResult) error
	CompactionHook func(context.Context, workflowCompactionHookInput) (
		workflowCompactionHookResult, error)
	Store        session.ExecutionStorage
	GenerationID uuid.UUID
	//nolint:containedctx // SDK activity contexts need worker cancellation.
	Lifecycle  context.Context
	AfterPause func(context.Context, uuid.UUID, []uuid.UUID) error
	// Preparation reads context and performs only idempotent durable writes.
	// External hooks run in tool activities.
	PrepareRound func(context.Context, workflowRoundInput) (
		json.RawMessage, error)
	CompactRound func(context.Context, workflowRoundInput) (
		workflowCompactionResult, error)
	ModelRound func(context.Context, workflowRoundInput) (
		workflowRoundResult, error)
	AfterModelRound func(context.Context, workflowRoundInput,
		workflowRoundResult) error
	ToolCall func(context.Context, workflowToolInput) (
		workflowToolResult, error)
	ToolPhase func(context.Context, workflowToolPhaseInput) (
		workflowToolPhaseState, error)
	AfterToolPhase func(context.Context, workflowToolPhaseInput,
		workflowToolPhaseState) error
	MergeRound func(
		json.RawMessage, []workflowToolInput, []workflowToolResult,
	) (json.RawMessage, error)
	BeforeFinish func(context.Context, workflowFinishInput) (
		workflowFinishResult, error)
	TerminalHooks func(context.Context, workflowFinishInput) (
		workflowFinishInput, error)
	// AfterFinish must tolerate repetition after a saved terminal checkpoint.
	AfterFinish func(context.Context, workflowFinishInput,
		workflowFinishResult) error
}

type workflowExecution struct {
	options workflowExecutionOptions
	mutex   sync.Mutex
	active  map[uuid.UUID]map[uuid.UUID]context.CancelFunc
}

func (o workflowExecutionOptions) validate() error {
	if o.Store == nil || o.Lifecycle == nil || o.GenerationID == uuid.Nil ||
		o.ModelRound == nil || o.ToolCall == nil || o.MergeRound == nil ||
		o.PrepareRound == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow execution dependencies")
	}

	return nil
}
