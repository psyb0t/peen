package protocol

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// Publisher receives durable records after the controller has written them.
//
// Publication happens after the write, never before, so a client can never see
// an event the database does not already hold.
type Publisher interface {
	PublishSessionEvents(
		ctx context.Context,
		sessionID uuid.UUID,
		events []session.EventInput,
	)
}

// Controller answers one registered worker's durable calls.
//
// It is constructed per connection and holds that worker's session and
// generation. Every request is checked against them, so a worker asking for a
// session it was not launched for is refused rather than served.
type Controller struct {
	store        *session.Store
	publisher    Publisher
	sessionID    uuid.UUID
	generationID uuid.UUID
}

// NewController binds one worker connection to its session.
func NewController(
	store *session.Store,
	publisher Publisher,
	sessionID uuid.UUID,
	generationID uuid.UUID,
) *Controller {
	return &Controller{
		store:        store,
		publisher:    publisher,
		sessionID:    sessionID,
		generationID: generationID,
	}
}

// HandleCommand is never called on the controller side. A worker does not send
// commands to its controller, so a command frame arriving here is a protocol
// violation rather than work to do.
func (c *Controller) HandleCommand(
	_ context.Context,
	command Command,
	_ json.RawMessage,
) (any, error) {
	return nil, ctxerrors.Wrapf(
		commerr.ErrPermissionDenied,
		"a worker may not send the %q command",
		command,
	)
}

// HandleCall serves one durable request from the worker.
func (c *Controller) HandleCall(
	ctx context.Context,
	method string,
	payload json.RawMessage,
) (any, error) {
	handlers := []func(
		context.Context,
		Method,
		json.RawMessage,
	) (any, bool, error){
		c.handleSessionCall,
		c.handleTurnCall,
		c.handleModelCall,
		c.handleJobCall,
		c.handleAgentRunCall,
		c.handleRecordCall,
	}

	for _, handle := range handlers {
		result, served, err := handle(ctx, Method(method), payload)
		if served {
			return result, err
		}
	}

	return nil, ctxerrors.Wrapf(
		commerr.ErrNotImplemented,
		"worker protocol method %q is not published",
		method,
	)
}

// assertSession refuses a request naming a session other than the one this
// connection registered for. It is the check that makes a private socket a
// real boundary rather than a convention.
func (c *Controller) assertSession(sessionID uuid.UUID) error {
	if sessionID == c.sessionID {
		return nil
	}

	return ctxerrors.Wrap(
		commerr.ErrPermissionDenied,
		"a worker may only act on the session it was launched for",
	)
}

func (c *Controller) assertLease(lease session.Lease) error {
	return c.assertSession(lease.SessionID)
}

//nolint:cyclop,exhaustive // One published method per branch reads as a table.
func (c *Controller) handleSessionCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodGetSession:
		return served(callSession(ctx, c, payload, c.store.Get))
	case MethodCompletedHistory:
		return served(callSession(ctx, c, payload, c.store.CompletedHistory))
	case MethodDrainSessionNotices:
		return served(callSession(
			ctx,
			c,
			payload,
			c.store.DrainSessionNotices,
		))
	case MethodCreateOrResume:
		return served(c.createOrResume(ctx, payload))
	case MethodListMessages:
		return served(callOptions(ctx, c, payload, c.store.ListMessages))
	case MethodListEvents:
		return served(callOptions(ctx, c, payload, c.store.ListEvents))
	case MethodListTurns:
		return served(callOptions(ctx, c, payload, c.store.ListTurns))
	case MethodListSessionNotices:
		return served(callOptions(
			ctx,
			c,
			payload,
			c.store.ListSessionNotices,
		))
	case MethodCreateSessionNotice:
		return served(callInput(ctx, c, payload, c.store.CreateSessionNotice))
	default:
		return nil, false, nil
	}
}

//nolint:exhaustive // One branch per published method.
func (c *Controller) handleTurnCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodAcquireTurn:
		return served(callInput(ctx, c, payload, c.store.AcquireTurn))
	case MethodAppendCheckpoint:
		return served(c.appendCheckpoint(ctx, payload))
	case MethodFinalizeTurn:
		return served(c.finalizeTurn(ctx, payload))
	case MethodReleaseTurn:
		return served(c.releaseTurn(payload))
	case MethodRecordTurnWorker:
		return served(c.recordTurnWorker(ctx, payload))
	case MethodCancelSession:
		return served(c.cancelSession(ctx, payload))
	default:
		return nil, false, nil
	}
}

//nolint:exhaustive,dupl // One branch per published method.
func (c *Controller) handleModelCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodCreateModelRun:
		return served(callInput(ctx, c, payload, c.store.CreateModelRun))
	case MethodFinalizeModelRun:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.FinalizeModelRun,
		))
	case MethodGetModelRun:
		return served(callChild(ctx, c, payload, c.store.GetModelRun))
	case MethodListModelRuns:
		return served(callOptions(ctx, c, payload, c.store.ListModelRuns))
	case MethodCreateModelCall:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.CreateModelCall,
		))
	case MethodFinalizeModelCall:
		return served(callNestedInput(
			ctx,
			c,
			payload,
			c.store.FinalizeModelCall,
		))
	case MethodRecordModelCallRetries:
		return served(callNestedWrite(
			ctx,
			c,
			payload,
			c.store.RecordModelCallRetries,
		))
	case MethodListModelCalls:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListModelCalls,
		))
	default:
		return nil, false, nil
	}
}

//nolint:exhaustive,dupl // One branch per published method.
func (c *Controller) handleJobCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodCreateJob:
		return served(callInput(ctx, c, payload, c.store.CreateJob))
	case MethodFinalizeJob:
		return served(callChildInput(ctx, c, payload, c.store.FinalizeJob))
	case MethodGetJob:
		return served(callChild(ctx, c, payload, c.store.GetJob))
	case MethodListJobs:
		return served(callOptions(ctx, c, payload, c.store.ListJobs))
	case MethodAppendJobOutput:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.AppendJobOutput,
		))
	case MethodListJobOutput:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListJobOutput,
		))
	case MethodRecordJobSignal:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.RecordJobSignal,
		))
	case MethodListJobSignalRequests:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListJobSignalRequests,
		))
	default:
		return nil, false, nil
	}
}

//nolint:exhaustive // One branch per published method.
func (c *Controller) handleAgentRunCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodCreateAgentRun:
		return served(callInput(ctx, c, payload, c.store.CreateAgentRun))
	case MethodFinalizeAgentRun:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.FinalizeAgentRun,
		))
	case MethodGetAgentRun:
		return served(callChild(ctx, c, payload, c.store.GetAgentRun))
	case MethodListAgentRuns:
		return served(callOptions(ctx, c, payload, c.store.ListAgentRuns))
	case MethodAppendAgentRunEvent:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.AppendAgentRunEvent,
		))
	case MethodListAgentRunEvents:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListAgentRunEvents,
		))
	case MethodCancelAgentRun:
		return served(c.cancelAgentRun(ctx, payload))
	default:
		return c.handleAgentRunTranscriptCall(ctx, method, payload)
	}
}

// handleAgentRunTranscriptCall serves the child's own transcript and
// compaction lineage, which are stored apart from the session's.
//
//nolint:exhaustive // One published method per branch reads as a table.
func (c *Controller) handleAgentRunTranscriptCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodAppendAgentRunMessages:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.AppendAgentRunMessages,
		))
	case MethodListAgentRunMessages:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListAgentRunMessages,
		))
	case MethodAgentRunHistory:
		return served(callChild(ctx, c, payload, c.store.AgentRunHistory))
	case MethodCreateAgentRunCompaction:
		return served(callChildInput(
			ctx,
			c,
			payload,
			c.store.CreateAgentRunCompaction,
		))
	case MethodLatestAgentRunCompaction:
		return served(callChild(
			ctx,
			c,
			payload,
			c.store.LatestAgentRunCompaction,
		))
	case MethodListAgentRunCompactions:
		return served(callChildOptions(
			ctx,
			c,
			payload,
			c.store.ListAgentRunCompactions,
		))
	case MethodGetAgentRunCompaction:
		return served(callNestedRead(
			ctx,
			c,
			payload,
			c.store.GetAgentRunCompaction,
		))
	default:
		return nil, false, nil
	}
}

//nolint:exhaustive // One published method per branch reads as a table.
func (c *Controller) handleRecordCall(
	ctx context.Context,
	method Method,
	payload json.RawMessage,
) (any, bool, error) {
	switch method {
	case MethodCreateCompaction:
		return served(callInput(ctx, c, payload, c.store.CreateCompaction))
	case MethodGetCompaction:
		return served(callChild(ctx, c, payload, c.store.GetCompaction))
	case MethodListCompactions:
		return served(callOptions(ctx, c, payload, c.store.ListCompactions))
	case MethodLatestCompaction:
		return served(callSession(ctx, c, payload, c.store.LatestCompaction))
	case MethodSaveContextSnapshot:
		return served(c.saveContextSnapshot(ctx, payload))
	case MethodSavePromptSnapshot:
		return served(c.savePromptSnapshot(ctx, payload))
	case MethodGetContextSnapshot:
		return served(callHash(ctx, c, payload, c.store.GetContextSnapshot))
	case MethodGetPromptSnapshot:
		return served(callHash(ctx, c, payload, c.store.GetPromptSnapshot))
	default:
		return nil, false, nil
	}
}

func (c *Controller) createOrResume(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[CreateOrResumeRequest](payload)
	if err != nil {
		return nil, err
	}

	// A worker opens only the session it was launched for. Passing its own ID
	// is what makes the call a resume rather than a way to mint sessions.
	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	opened, err := c.store.CreateOrResume(ctx, &c.sessionID, request.Options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resume the worker session")
	}

	return opened, nil
}

func (c *Controller) appendCheckpoint(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[CheckpointRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertLease(request.Lease); err != nil {
		return nil, err
	}

	if err := c.store.AppendCheckpoint(
		ctx,
		request.Lease,
		request.Messages,
		request.Events,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "append the turn checkpoint")
	}

	// Publication follows the write, so a client never sees an event the
	// database does not already hold.
	c.publish(ctx, request.Events)

	return Ack{}, nil
}

func (c *Controller) finalizeTurn(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[LeaseInputRequest[session.FinalizeTurnInput]](
		payload,
	)
	if err != nil {
		return nil, err
	}

	if err := c.assertLease(request.Lease); err != nil {
		return nil, err
	}

	if err := c.store.FinalizeTurn(
		ctx,
		request.Lease,
		request.Input,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize the turn")
	}

	return Ack{}, nil
}

func (c *Controller) releaseTurn(payload json.RawMessage) (any, error) {
	request, err := decode[LeaseRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertLease(request.Lease); err != nil {
		return nil, err
	}

	c.store.ReleaseTurn(request.Lease)

	return Ack{}, nil
}

func (c *Controller) recordTurnWorker(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[RecordTurnWorkerRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertLease(request.Lease); err != nil {
		return nil, err
	}

	// A worker may only claim its own generation. Without this it could
	// relabel a turn as having run in an environment it did not.
	if request.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"a worker may only record its own generation",
		)
	}

	if err := c.store.RecordTurnWorkerGeneration(
		ctx,
		request.Lease,
		request.GenerationID,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "record the turn worker generation")
	}

	return Ack{}, nil
}

func (c *Controller) cancelSession(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[SessionRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	requested, err := c.store.Cancel(ctx, request.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "record the cancellation request")
	}

	return BoolResult{Value: requested}, nil
}

func (c *Controller) cancelAgentRun(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[ChildRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	run, requested, err := c.store.RequestAgentRunCancellation(
		ctx,
		request.SessionID,
		request.ChildID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "request agent run cancellation")
	}

	return AgentRunCancellationResult{Run: run, Requested: requested}, nil
}

func (c *Controller) saveContextSnapshot(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[SnapshotRequest[*models.ContextSnapshot]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	if request.Snapshot == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"context snapshot is required",
		)
	}

	if err := c.store.SaveContextSnapshot(ctx, request.Snapshot); err != nil {
		return nil, ctxerrors.Wrap(err, "save the context snapshot")
	}

	return Ack{}, nil
}

func (c *Controller) savePromptSnapshot(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[SnapshotRequest[*models.PromptSnapshot]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	if request.Snapshot == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"prompt snapshot is required",
		)
	}

	if err := c.store.SavePromptSnapshot(ctx, request.Snapshot); err != nil {
		return nil, ctxerrors.Wrap(err, "save the prompt snapshot")
	}

	return Ack{}, nil
}

func (c *Controller) publish(
	ctx context.Context,
	events []session.EventInput,
) {
	if c.publisher == nil || len(events) == 0 {
		return
	}

	c.publisher.PublishSessionEvents(ctx, c.sessionID, events)
}

// served adapts a two-value handler result to the three-value dispatch shape.
func served(result any, err error) (any, bool, error) {
	return result, true, err
}

//nolint:ireturn // The decoded type is the caller's own request struct.
func decode[T any](payload json.RawMessage) (T, error) {
	decoded := *new(T)

	if len(payload) == 0 {
		return decoded, nil
	}

	if err := json.Unmarshal(payload, &decoded); err != nil {
		return decoded, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"decode worker protocol request",
		)
	}

	return decoded, nil
}

// The call* helpers below turn one store method into a dispatch entry. Each
// decodes its envelope, checks the session, and forwards. They exist so the
// session check cannot be forgotten on a new method.

func callSession[R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID) (R, error),
) (any, error) {
	request, err := decode[SessionRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID)
}

func callChild[R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, uuid.UUID) (R, error),
) (any, error) {
	request, err := decode[ChildRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.ChildID)
}

func callOptions[O, R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, O) (R, error),
) (any, error) {
	request, err := decode[OptionsRequest[O]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.Options)
}

func callChildOptions[O, R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, uuid.UUID, O) (R, error),
) (any, error) {
	request, err := decode[ChildOptionsRequest[O]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.ChildID, request.Options)
}

func callInput[I, R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, I) (R, error),
) (any, error) {
	request, err := decode[InputRequest[I]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.Input)
}

func callChildInput[I, R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, uuid.UUID, I) (R, error),
) (any, error) {
	request, err := decode[ChildInputRequest[I]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.ChildID, request.Input)
}

func callNestedInput[I, R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		I,
	) (R, error),
) (any, error) {
	request, err := decode[NestedChildInputRequest[I]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(
		ctx,
		request.SessionID,
		request.ChildID,
		request.NestedID,
		request.Input,
	)
}

// callNestedWrite serves a nested write whose only outcome is success or
// failure.
// callNestedRead serves a read addressing a record inside a child record,
// which is one compaction inside one agent run.
func callNestedRead[R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
	) (R, error),
) (any, error) {
	request, err := decode[NestedChildRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(
		ctx,
		request.SessionID,
		request.ChildID,
		request.NestedID,
	)
}

func callNestedWrite[I any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		I,
	) error,
) (any, error) {
	request, err := decode[NestedChildInputRequest[I]](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return nil, operation(
		ctx,
		request.SessionID,
		request.ChildID,
		request.NestedID,
		request.Input,
	)
}

func callHash[R any](
	ctx context.Context,
	c *Controller,
	payload json.RawMessage,
	operation func(context.Context, uuid.UUID, string) (R, error),
) (any, error) {
	request, err := decode[HashRequest](payload)
	if err != nil {
		return nil, err
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, err
	}

	return operation(ctx, request.SessionID, request.Hash)
}
