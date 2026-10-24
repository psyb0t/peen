package protocol

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// Method names one durable operation a worker may ask its controller to
// perform. The set is closed: a worker cannot reach a store method the
// controller did not publish here, which is what keeps the protocol from
// becoming a generic database connection.
type Method string

const (
	MethodGetSession        Method = "session.get"
	MethodGetDirectoryState Method = "session.directoryState"
	MethodCreateOrResume    Method = "session.createOrResume"
	MethodCompletedHistory  Method = "session.completedHistory"

	MethodAcquireTurn      Method = "turn.acquire"
	MethodAppendCheckpoint Method = "turn.appendCheckpoint"
	MethodFinalizeTurn     Method = "turn.finalize"
	MethodReleaseTurn      Method = "turn.release"
	MethodEndRunningTurn   Method = "turn.endRunning"
	MethodRecordTurnWorker Method = "turn.recordWorkerGeneration"
	MethodCancelSession    Method = "turn.cancel"
	MethodListTurns        Method = "turn.list"

	MethodListMessages Method = "message.list"
	MethodListEvents   Method = "event.list"

	MethodCreateModelRun         Method = "modelRun.create"
	MethodFinalizeModelRun       Method = "modelRun.finalize"
	MethodGetModelRun            Method = "modelRun.get"
	MethodListModelRuns          Method = "modelRun.list"
	MethodCreateModelCall        Method = "modelCall.create"
	MethodFinalizeModelCall      Method = "modelCall.finalize"
	MethodRecordModelCallRetries Method = "modelCall.recordRetries"
	MethodListModelCalls         Method = "modelCall.list"

	MethodCreateJob             Method = "job.create"
	MethodFinalizeJob           Method = "job.finalize"
	MethodGetJob                Method = "job.get"
	MethodListJobs              Method = "job.list"
	MethodAppendJobOutput       Method = "job.appendOutputLines"
	MethodListJobOutput         Method = "job.listOutput"
	MethodRecordJobSignal       Method = "job.recordSignal"
	MethodListJobSignalRequests Method = "job.listSignalRequests"

	MethodCreateAgentRun          Method = "agentRun.create"
	MethodClaimAgentRun           Method = "agentRun.claim"
	MethodPrepareAgentRunRecovery Method = "agentRun.prepareRecovery"
	MethodFinalizeAgentRun        Method = "agentRun.finalize"
	MethodGetAgentRun             Method = "agentRun.get"
	MethodListAgentRuns           Method = "agentRun.list"
	MethodAppendAgentRunEvent     Method = "agentRun.appendEvent"
	MethodListAgentRunEvents      Method = "agentRun.listEvents"
	MethodCancelAgentRun          Method = "agentRun.requestCancellation"

	MethodOpenAgentRunQuestions Method = "agentRun.openQuestions"

	MethodAppendAgentRunMessages   Method = "agentRun.appendMessages"
	MethodListAgentRunMessages     Method = "agentRun.listMessages"
	MethodAgentRunHistory          Method = "agentRun.history"
	MethodCreateAgentRunCompaction Method = "agentRun.createCompaction"
	MethodLatestAgentRunCompaction Method = "agentRun.latestCompaction"
	MethodListAgentRunCompactions  Method = "agentRun.listCompactions"
	MethodGetAgentRunCompaction    Method = "agentRun.getCompaction"

	MethodCreateCompaction Method = "compaction.create"
	MethodGetCompaction    Method = "compaction.get"
	MethodListCompactions  Method = "compaction.list"
	MethodLatestCompaction Method = "compaction.latest"

	MethodSaveContextSnapshot Method = "snapshot.saveContext"
	MethodGetContextSnapshot  Method = "snapshot.getContext"
	MethodSavePromptSnapshot  Method = "snapshot.savePrompt"
	MethodGetPromptSnapshot   Method = "snapshot.getPrompt"

	MethodCreateSessionNotice Method = "notice.create"
	MethodListSessionNotices  Method = "notice.list"
	MethodDrainSessionNotices Method = "notice.drain"

	MethodPutContentBlobs Method = "contentBlob.put"
	MethodGetContentBlobs Method = "contentBlob.get"
)

// The call envelopes below cover every shape the store surface takes. Reusing
// six generic shapes instead of one struct per method keeps the wire contract
// small enough to read in one sitting.

// SessionRequest addresses one session.
//
// Every envelope carries the session ID even though the socket is already
// bound to one session. The controller checks the two agree and refuses the
// frame when they do not, so a worker that guesses another session's ID still
// gets nowhere.
type SessionRequest struct {
	SessionID uuid.UUID `json:"sessionId"`
}

// ChildRequest addresses one record inside a session, such as a job, a model
// run, an agent run, or a compaction.
type ChildRequest struct {
	SessionID uuid.UUID `json:"sessionId"`
	ChildID   uuid.UUID `json:"childId"`
}

// OptionsRequest addresses one session with read options.
type OptionsRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	Options   T         `json:"options"`
}

// ChildOptionsRequest addresses one child record with read options.
type ChildOptionsRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	ChildID   uuid.UUID `json:"childId"`
	Options   T         `json:"options"`
}

// InputRequest addresses one session with a write payload.
type InputRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	Input     T         `json:"input"`
}

// ChildInputRequest addresses one child record with a write payload.
type ChildInputRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	ChildID   uuid.UUID `json:"childId"`
	Input     T         `json:"input"`
}

// NestedChildInputRequest addresses a record inside a child record, which is
// only the model call inside a model run.
type NestedChildInputRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	ChildID   uuid.UUID `json:"childId"`
	NestedID  uuid.UUID `json:"nestedId"`
	Input     T         `json:"input"`
}

// NestedChildRequest addresses a record inside a child record for a read,
// which is a compaction inside one agent run.
type NestedChildRequest struct {
	SessionID uuid.UUID `json:"sessionId"`
	ChildID   uuid.UUID `json:"childId"`
	NestedID  uuid.UUID `json:"nestedId"`
}

// LeaseRequest addresses one turn lease.
type LeaseRequest struct {
	Lease session.Lease `json:"lease"`
}

// EndTurnRequest ends one running turn of the worker's session without its
// lease, for a turn Stop or a failed workflow ended.
type EndTurnRequest struct {
	SessionID uuid.UUID       `json:"sessionId"`
	TurnID    uuid.UUID       `json:"turnId"`
	End       session.TurnEnd `json:"end"`
}

// LeaseInputRequest addresses one turn lease with a write payload.
type LeaseInputRequest[T any] struct {
	Lease session.Lease `json:"lease"`
	Input T             `json:"input"`
}

// CheckpointRequest appends transcript records under one lease.
type CheckpointRequest struct {
	Lease    session.Lease          `json:"lease"`
	Messages []session.MessageInput `json:"messages"`
	Events   []session.EventInput   `json:"events"`
}

// RecordTurnWorkerRequest names the worker generation that ran one turn.
type RecordTurnWorkerRequest struct {
	Lease        session.Lease `json:"lease"`
	GenerationID uuid.UUID     `json:"generationId"`
}

// SnapshotRequest stores one immutable snapshot. The session ID travels beside
// it so the controller can check ownership before writing.
type SnapshotRequest[T any] struct {
	SessionID uuid.UUID `json:"sessionId"`
	Snapshot  T         `json:"snapshot"`
}

// HashRequest reads one content-addressed snapshot.
type HashRequest struct {
	SessionID uuid.UUID `json:"sessionId"`
	Hash      string    `json:"hash"`
}

// ContentBlobsRequest stores the long strings of a compacted payload.
type ContentBlobsRequest struct {
	SessionID uuid.UUID          `json:"sessionId"`
	Blobs     []contentblob.Blob `json:"blobs"`
}

// ContentBlobHashesRequest reads stored strings back by hash.
type ContentBlobHashesRequest struct {
	SessionID uuid.UUID `json:"sessionId"`
	Hashes    []string  `json:"hashes"`
}

// CreateOrResumeRequest opens the worker's own session. The controller ignores
// any other session ID here, because a worker opens only what it was launched
// for.
type CreateOrResumeRequest struct {
	SessionID uuid.UUID                  `json:"sessionId"`
	Options   session.OpenSessionOptions `json:"options"`
}

// BoolResult answers an operation whose only outcome is whether it happened.
type BoolResult struct {
	Value bool `json:"value"`
}

// AgentRunCancellationResult answers a child-run cancellation request.
type AgentRunCancellationResult struct {
	Run       *models.AgentRun `json:"run"`
	Requested bool             `json:"requested"`
}
