package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// ExecutionStorage is the workflow checkpoint and control boundary.
// Workers reach the same operations over their authenticated session socket.
type ExecutionStorage interface {
	ExecutionRecords
	ExecutionActivities
	ExecutionTurnLeases
	ExecutionInboxStorage
	ExecutionCompletionStorage
}

type ExecutionCompletionStorage interface {
	ReserveExecutionCompletion(context.Context, uuid.UUID, ActivityLease) (
		*ExecutionCompletionSeal, error)
}

type ExecutionInboxStorage interface {
	ListDeferredExecutionMessages(context.Context, uuid.UUID,
		ListEventsOptions) (*ExecutionInboxPage, error)
	DeferExecutionMessage(context.Context, uuid.UUID, Lease,
		AcceptExecutionMessageInput) (*ExecutionInboxAcceptance, error)
	GetExecutionMessageByRequest(context.Context, uuid.UUID, uuid.UUID,
		uuid.UUID) (*models.ExecutionInboxMessage, error)
	AcceptExecutionMessage(context.Context, uuid.UUID, Lease,
		AcceptExecutionMessageInput) (*ExecutionInboxAcceptance, error)
	ListExecutionMessages(context.Context, uuid.UUID, uuid.UUID,
		ListExecutionInboxOptions) (*ExecutionInboxPage, error)
	DeliverExecutionMessages(context.Context, uuid.UUID,
		DeliverExecutionMessagesInput) (*ExecutionInboxDelivery, error)
}

type ChildExecutionInboxStorage interface {
	AcceptChildExecutionMessage(context.Context, uuid.UUID, uuid.UUID,
		AcceptExecutionMessageInput) (*ExecutionInboxAcceptance, error)
	DeliverChildExecutionMessages(context.Context, uuid.UUID, ActivityLease,
		[]uuid.UUID) (*ExecutionInboxDelivery, error)
}

var _ ChildExecutionInboxStorage = (*Store)(nil)

type ExecutionTurnLeases interface {
	AcquireDeferredExecutionTurn(context.Context, uuid.UUID, uuid.UUID,
		StartTurnInput) (*DeferredTurnAdmission, error)
	GetExecutionRootTurn(context.Context, uuid.UUID, uuid.UUID) (
		*models.Turn, error)
	RestoreExecutionTurn(context.Context, uuid.UUID, uuid.UUID,
		RestoreExecutionTurnInput) (Lease, error)
	GetExecutionTurnLease(context.Context, uuid.UUID, uuid.UUID) (
		*models.ExecutionTurnLease, error)
}

type ExecutionRecords interface {
	CreateExecution(context.Context, uuid.UUID, CreateExecutionInput) (
		*models.Execution, error)
	GetExecution(context.Context, uuid.UUID, uuid.UUID) (
		*models.Execution, error)
	SearchExecutions(context.Context, uuid.UUID, SearchExecutionsOptions) (
		*ExecutionPage, error)
	PauseExecution(context.Context, uuid.UUID, uuid.UUID, string) (
		[]uuid.UUID, error)
	ResumeExecution(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error)
	ListExecutionEvents(
		context.Context, uuid.UUID, uuid.UUID, ListEventsOptions,
	) (*ExecutionEventPage, error)
}

type ExecutionActivities interface {
	BeginExecutionActivity(
		context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ScheduleActivityInput,
	) (*ActivityClaim, error)
	CompleteExecutionActivity(
		context.Context, uuid.UUID, ActivityLease, CompleteActivityInput,
	) (*models.ExecutionActivity, error)
	InterruptExecutionActivity(context.Context, uuid.UUID, ActivityLease) error
	ResolveExecutionActivity(
		context.Context, uuid.UUID, uuid.UUID, ResolveActivityInput,
	) (*models.ExecutionActivity, error)
	GetExecutionActivity(context.Context, uuid.UUID, uuid.UUID) (
		*models.ExecutionActivity, error)
	ListExecutionActivities(
		context.Context, uuid.UUID, uuid.UUID, ListEventsOptions,
	) (*ExecutionActivityPage, error)
}

var _ ExecutionStorage = (*Store)(nil)
