package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) deliverNativeChildMessage(
	ctx context.Context,
	sessionID, runID, requestID uuid.UUID,
	text string,
) (bool, error) {
	native := r.nativeWorkflowRunner()
	if native == nil {
		return false, nil
	}

	if sessionID != r.sessionID {
		return true, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"child message belongs to another session")
	}

	// Acceptance and hydration share this lock so neither can miss a message.
	r.childMessageMutex.Lock()
	defer r.childMessageMutex.Unlock()

	executionID := uuid.NewSHA1(runID, []byte(childWorkflowIdentity))

	execution, err := native.engine.options.Store.GetExecution(ctx, sessionID,
		executionID)
	if errors.Is(err, commerr.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return true, ctxerrors.Wrap(err, "read native child recipient")
	}

	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil || *execution.AgentRunID != runID {
		return true, ctxerrors.Wrap(commerr.ErrConflict,
			"native child recipient identity changed")
	}

	input := session.AcceptExecutionMessageInput{
		RequestID: requestID, Message: text, Capacity: r.maxQueuedUserMessages,
	}

	input, err = native.retainChildMessageDirectory(ctx, execution, input)
	if err != nil {
		return true, ctxerrors.Wrap(err, "restore child acceptance directory")
	}

	return true, r.acceptNativeChildMessage(ctx, execution, input)
}

func (n *nativeWorkflowRunner) retainChildMessageDirectory(
	ctx context.Context,
	execution *models.Execution,
	input session.AcceptExecutionMessageInput,
) (session.AcceptExecutionMessageInput, error) {
	saved, err := n.engine.options.Store.GetExecutionMessageByRequest(ctx,
		execution.SessionID, execution.ID, input.RequestID)
	if errors.Is(err, commerr.ErrNotFound) {
		return input, nil
	}

	if err != nil {
		return input, ctxerrors.Wrap(err, "read retained child request")
	}

	input.WorkingDirectory = saved.WorkingDirectory

	return input, nil
}

func (r *Runtime) acceptNativeChildMessage(
	ctx context.Context,
	execution *models.Execution,
	input session.AcceptExecutionMessageInput,
) error {
	registry := r.existingAgentRuns(execution.SessionID)
	if registry != nil && !childInboxUnavailable(execution.State) {
		if run, found := registry.Get(*execution.AgentRunID); found &&
			run.inbox != nil {
			handled, err := run.inbox.acceptWorkflowMessage(ctx, input)
			if handled {
				return err
			}
		}
	}

	writer, ok := r.store.(session.ChildExecutionInboxStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native child message storage")
	}

	if input.WorkingDirectory == "" {
		input.WorkingDirectory = execution.WorkingDirectory
	}

	_, err := writer.AcceptChildExecutionMessage(ctx, execution.SessionID,
		execution.ID, input)
	if err != nil {
		return ctxerrors.Wrap(err, "accept message before child restoration")
	}

	return nil
}

func (i *agentInbox) acceptWorkflowMessage(
	ctx context.Context,
	input session.AcceptExecutionMessageInput,
) (bool, error) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.workflow == nil {
		return false, nil
	}

	if i.closed {
		return true, ctxerrors.Wrap(errAgentRunFinished(),
			"accept native child message")
	}

	return true, i.workflow.accept(ctx, input)
}
