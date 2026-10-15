package protocol

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const methodExecutionStore Method = "execution.store"

type executionOperation string

const (
	executionCreate               executionOperation = "create"
	executionGet                  executionOperation = "get"
	executionSearch               executionOperation = "search"
	executionPause                executionOperation = "pause"
	executionResume               executionOperation = "resume"
	executionResumeFromActivity   executionOperation = "resumeFromActivity"
	executionResolveFromActivity  executionOperation = "resolveFromActivity"
	executionEvents               executionOperation = "events"
	executionReserveCompletion    executionOperation = "reserveCompletion"
	executionBeginActivity        executionOperation = "beginActivity"
	executionCompleteActivity     executionOperation = "completeActivity"
	executionInterruptActivity    executionOperation = "interruptActivity"
	executionResolveActivity      executionOperation = "resolveActivity"
	executionGetActivity          executionOperation = "getActivity"
	executionListActivities       executionOperation = "listActivities"
	executionRestoreTurn          executionOperation = "restoreTurn"
	executionGetTurnLease         executionOperation = "getTurnLease"
	executionGetRootTurn          executionOperation = "getRootTurn"
	executionAcceptMessage        executionOperation = "acceptMessage"
	executionDeferMessage         executionOperation = "deferMessage"
	executionAcquireDeferredTurn  executionOperation = "acquireDeferredTurn"
	executionListDeferredMessages executionOperation = "listDeferredMessages"
	executionAcceptChildMessage   executionOperation = "acceptChildMessage"
	executionListMessages         executionOperation = "listMessages"
	executionGetMessageByRequest  executionOperation = "getMessageByRequest"
	executionDeliverMessages      executionOperation = "deliverMessages"
	executionDeliverChildMessages executionOperation = "deliverChildMessages"
)

type executionRequest struct {
	InboxID         uuid.UUID                             `json:"inboxId"`
	StartTurn       session.StartTurnInput                `json:"startTurn"`
	Operation       executionOperation                    `json:"operation"`
	ExecutionID     uuid.UUID                             `json:"executionId"`
	ActivityID      uuid.UUID                             `json:"activityId"`
	RequestID       uuid.UUID                             `json:"requestId"`
	GenerationID    uuid.UUID                             `json:"generationId"`
	Create          session.CreateExecutionInput          `json:"create"`
	Search          session.SearchExecutionsOptions       `json:"search"`
	Reason          string                                `json:"reason"`
	Page            session.ListEventsOptions             `json:"page"`
	Begin           session.ScheduleActivityInput         `json:"begin"`
	Lease           session.ActivityLease                 `json:"lease"`
	Complete        session.CompleteActivityInput         `json:"complete"`
	Resolve         session.ResolveActivityInput          `json:"resolve"`
	ResumeControl   session.ResumeExecutionInput          `json:"resumeControl"`
	ResolveControl  session.ResolveExecutionControlInput  `json:"resolveControl"` //nolint:lll // Keep the wire tag beside its field.
	RestoreTurn     session.RestoreExecutionTurnInput     `json:"restoreTurn"`
	TurnLease       session.Lease                         `json:"turnLease"`
	AcceptMessage   session.AcceptExecutionMessageInput   `json:"acceptMessage"`
	InboxPage       session.ListExecutionInboxOptions     `json:"inboxPage"`
	DeliverMessages session.DeliverExecutionMessagesInput `json:"deliverMessages"` //nolint:lll // Keep the wire tag beside its field.
}
