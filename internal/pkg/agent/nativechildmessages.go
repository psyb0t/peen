package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// deliverNativeChildMessage queues text in a durable child's inbox. A paused
// child is resumed by the message, reads why it was paused first, and has its
// workflow started again.
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

	handled, resumed, err := r.acceptNativeChildRecipientMessage(ctx, native,
		sessionID, runID, requestID, text)
	if !handled || err != nil || len(resumed) == 0 {
		return handled, err
	}

	// Outside childMessageMutex, because starting the resumed child hydrates
	// its inbox under that lock.
	if err := native.nudgeAdmittedExecutions(ctx); err != nil {
		return true, ctxerrors.Wrap(err, "start resumed child")
	}

	ctxscope.GetLogger(ctx).Info("message resumed paused child",
		"agent_run_id", runID, "resumed_count", len(resumed))

	return true, nil
}

func (r *Runtime) acceptNativeChildRecipientMessage(
	ctx context.Context,
	native *nativeWorkflowRunner,
	sessionID, runID, requestID uuid.UUID,
	text string,
) (bool, []uuid.UUID, error) {
	// Acceptance and hydration share this lock so neither can miss a message.
	r.childMessageMutex.Lock()
	defer r.childMessageMutex.Unlock()

	executionID := uuid.NewSHA1(runID, []byte(childWorkflowIdentity))

	execution, err := native.engine.options.Store.GetExecution(ctx, sessionID,
		executionID)
	if errors.Is(err, commerr.ErrNotFound) {
		return false, nil, nil
	}

	if err != nil {
		return true, nil, ctxerrors.Wrap(err, "read native child recipient")
	}

	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil || *execution.AgentRunID != runID {
		return true, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"native child recipient identity changed")
	}

	input := session.AcceptExecutionMessageInput{
		RequestID: requestID, Message: text, Capacity: r.maxQueuedUserMessages,
	}

	if execution.State == models.ExecutionStatePaused {
		input.ResumeNotice, err = renderPausedNotice(execution.PauseReason)
		if err != nil {
			return true, nil, ctxerrors.Wrap(err, "render child pause notice")
		}
	}

	input, err = native.retainChildMessageDirectory(ctx, execution, input)
	if err != nil {
		return true, nil, ctxerrors.Wrap(err,
			"restore child acceptance directory")
	}

	resumed, err := r.acceptNativeChildMessage(ctx, execution, input)

	return true, resumed, err
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

// acceptNativeChildMessage queues input and returns the executions it
// resumed. A paused child bypasses its live inbox, which a pause leaves
// closed, and is accepted durably.
func (r *Runtime) acceptNativeChildMessage(
	ctx context.Context,
	execution *models.Execution,
	input session.AcceptExecutionMessageInput,
) ([]uuid.UUID, error) {
	registry := r.existingAgentRuns(execution.SessionID)
	if registry != nil && !childInboxUnavailable(execution.State) {
		if run, found := registry.Get(*execution.AgentRunID); found &&
			run.inbox != nil {
			handled, err := run.inbox.acceptWorkflowMessage(ctx, input)
			if handled {
				return nil, err
			}
		}
	}

	writer, ok := r.store.(session.ChildExecutionInboxStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native child message storage")
	}

	if input.WorkingDirectory == "" {
		input.WorkingDirectory = execution.WorkingDirectory
	}

	accepted, err := writer.AcceptChildExecutionMessage(ctx,
		execution.SessionID, execution.ID, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err,
			"accept message before child restoration")
	}

	return accepted.Resumed, nil
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
