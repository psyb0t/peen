package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) restoreRootWorkflowTurn(
	ctx context.Context,
	input workflowExecutionInput,
	holder session.RestoreExecutionTurnInput,
	sink EventSink,
) (*preparedTurn, *directoryScope, error) {
	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return nil, nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"root restoration requires execution storage")
	}

	conversation, original, err := r.readRootWorkflowBootstrap(ctx, store,
		input)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read root restoration bootstrap")
	}

	seed, err := r.rootWorkflowSeed(ctx, conversation, original, sink)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "build root restoration seed")
	}

	restored, scope, err := r.restoreRootWorkflowScope(ctx, seed, conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "rebuild saved root scope")
	}

	lease, err := store.RestoreExecutionTurn(ctx, input.SessionID,
		input.ExecutionID, holder)
	if err != nil {
		if errors.Is(err, session.ErrSessionBusy) {
			err = errors.Join(err, errRootAdmissionBusy)
		}

		return nil, nil, ctxerrors.Wrap(err, "claim restored root turn")
	}

	restored.lease = lease
	restored.turn.lease = lease
	restored.attachPublisher(context.WithoutCancel(ctx))

	if err := r.bindRootWorkflowInbox(ctx, restored,
		input.ExecutionID); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "bind restored root inbox")
	}

	return restored, scope, nil
}

func (r *Runtime) readRootWorkflowBootstrap(
	ctx context.Context,
	store session.ExecutionStorage,
	input workflowExecutionInput,
) (workflowConversation, *models.Turn, error) {
	var conversation workflowConversation
	if !validWorkflowExecutionInput(input) {
		return conversation, nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root restoration execution identity")
	}

	if err := json.Unmarshal(input.Context, &conversation); err != nil {
		return conversation, nil, ctxerrors.Wrap(err,
			"decode root restoration context")
	}

	if !conversation.Root.hasSupportedVersion() ||
		conversation.Root.RootAgent == "" {
		return conversation, nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root restoration bootstrap is missing")
	}

	execution, err := store.GetExecution(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return conversation, nil, ctxerrors.Wrap(err, "read root execution")
	}

	if conversation.Root.Message != execution.Task {
		return conversation, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"saved root message differs from accepted task")
	}

	if err := validateRetainedRootAgent(conversation.Root,
		execution); err != nil {
		return conversation, nil, ctxerrors.Wrap(err,
			"validate retained root agent")
	}

	original, err := store.GetExecutionRootTurn(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return conversation, nil, ctxerrors.Wrap(err,
			"read root execution turn")
	}

	return conversation, original, nil
}

func (r *Runtime) rootWorkflowSeed(
	ctx context.Context,
	conversation workflowConversation,
	original *models.Turn,
	sink EventSink,
) (*preparedTurn, error) {
	if err := r.validateRootBootstrap(conversation.Root, original); err != nil {
		return nil, ctxerrors.Wrap(err, "validate saved root bootstrap")
	}

	if conversation.Runtime == nil || conversation.SystemPrompt == "" {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved root runtime context is missing")
	}

	if err := validateWorkflowCompactionConversation(conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "validate restored compaction context")
	}

	input := conversation.Root.request(sink)
	if err := r.validateTurnInput(input); err != nil {
		return nil, ctxerrors.Wrap(err, "validate restored root input")
	}

	storedSession, err := r.store.Get(ctx, original.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read restored root session")
	}

	model, err := r.models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve saved root model")
	}

	executor, err := r.newTurnExecutor(turnBasis{
		storedSession: storedSession,
		workspace:     conversation.Runtime.Directory.Directory,
	}, original.SessionID, original.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build restored root executor")
	}

	prepared := r.rootWorkflowPrepared(input, conversation, original,
		storedSession)
	prepared.model = model
	prepared.executor = executor

	return prepared, nil
}

func (r *Runtime) rootWorkflowPrepared(
	input TurnRequest,
	conversation workflowConversation,
	original *models.Turn,
	storedSession *models.Session,
) *preparedTurn {
	prepared := &preparedTurn{
		workflowInbox: true,
		rootAgent:     conversation.Root.RootAgent,
		input:         input, origin: input.Origin,
		modelReference:     conversation.ModelReference,
		reasoningEffort:    conversation.ReasoningEffort,
		eventBus:           r.eventBus,
		drainNotices:       r.drainPendingEvents,
		workerGenerationID: workerGenerationReference(r.workerGenerationID),
		opened: &session.OpenSessionResult{
			Session: storedSession, Created: conversation.Root.SessionCreated,
		},
		turn: &runtimeTurn{
			store: r.store, requestID: original.RequestID, sink: input.OnEvent,
			triggeringEventID:  input.SourceEventID,
			observer:           r.sessionObserver(original.SessionID),
			workerGenerationID: workerGenerationReference(r.workerGenerationID),
		},
	}
	prepared.lease.SessionID = original.SessionID
	prepared.lease.TurnID = original.ID

	return prepared
}

func (r *Runtime) validateRootBootstrap(
	saved *workflowRootBootstrap,
	original *models.Turn,
) error {
	if !saved.hasSupportedVersion() || saved.RootAgent == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved root bootstrap version")
	}

	if saved.SessionID != original.SessionID || saved.TurnID != original.ID ||
		saved.RequestID != original.RequestID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved root bootstrap differs from original turn")
	}

	return nil
}

func validateRetainedRootAgent(
	saved *workflowRootBootstrap,
	execution *models.Execution,
) error {
	var retained struct {
		Root *workflowRootBootstrap `json:"root"`
	}
	if err := json.Unmarshal([]byte(execution.ContextJSON),
		&retained); err != nil {
		return ctxerrors.Wrap(err, "decode retained root identity")
	}

	if !retained.Root.hasSupportedVersion() ||
		retained.Root.RootAgent == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"retained root identity is missing")
	}

	if saved.RootAgent != retained.Root.RootAgent {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved root agent differs from retained execution")
	}

	return nil
}
