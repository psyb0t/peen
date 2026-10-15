package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const childWorkflowIdentity = "childWorkflowV1"

func (n *nativeWorkflowRunner) launchChild(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	launch preparedChildLaunch,
	task string,
	sender agentMessage,
) (launchAgentOutput, error) {
	parent, err := n.childSubmissionParent(ctx, deps)
	if err != nil {
		return launchAgentOutput{}, ctxerrors.Wrap(err,
			"validate native child parent")
	}

	runtime := n.dispatch.runtime

	run, runCtx, sink, err := runtime.startChildAgentRun(ctx, deps, input,
		launch)
	if err != nil {
		return launchAgentOutput{}, ctxerrors.Wrap(err,
			"start native child run")
	}

	runCtx = childAgentContext(runCtx, run, launch.definition, launch.depth,
		input.Task)

	actor, err := n.prepareChildActor(runCtx, deps, launch, task, run, sink)
	if err != nil {
		runtime.endChildAgentRun(runCtx, launch.registry, sink, run, nil, err)

		return launchAgentOutput{}, ctxerrors.Wrap(err,
			"prepare accepted native child")
	}

	execution, err := n.submitChild(runCtx, parent.ID, actor, launch)
	if err != nil {
		if execution == nil {
			runtime.endChildAgentRun(runCtx, launch.registry, sink, run,
				nil, err)
		}

		return launchAgentOutput{}, ctxerrors.Wrap(err,
			"submit accepted native child")
	}

	return launchAgentOutput{
		AgentRunID: run.ID, Name: run.Name, Definition: run.Definition,
		ParentAgentRunID: sender.Run,
	}, nil
}

func (n *nativeWorkflowRunner) childSubmissionParent(
	ctx context.Context,
	deps *launchAgentDeps,
) (*models.Execution, error) {
	if deps == nil || deps.sessionID != n.dispatch.runtime.sessionID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"child submission belongs to another session")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok || lease.GenerationID != n.engine.options.GenerationID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"child submission requires a current workflow activity")
	}

	store := n.engine.options.Store

	activity, err := store.GetExecutionActivity(ctx, deps.sessionID, lease.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child launch activity")
	}

	saved, err := decodeChildSubmissionCall(activity)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode child launch activity")
	}

	if !sameChildSubmissionCall(ctx, activity, lease, saved) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child launch activity no longer owns this call")
	}

	parent, err := store.GetExecution(ctx, deps.sessionID, activity.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child parent execution")
	}

	if !sameChildSubmissionParent(ctx, deps, parent) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child parent lineage or state changed")
	}

	return parent, nil
}

func sameChildSubmissionParent(
	ctx context.Context,
	deps *launchAgentDeps,
	parent *models.Execution,
) bool {
	return parent.RootTurnID == deps.parentTurnID &&
		sameChildBootstrapParent(parent.AgentRunID,
			parentAgentRunIDFromContext(ctx)) &&
		!childInboxUnavailable(parent.State)
}

func sameChildSubmissionCall(
	ctx context.Context,
	activity *models.ExecutionActivity,
	lease session.ActivityLease,
	saved workflowToolInput,
) bool {
	return sameChildToolActivity(activity, lease, saved,
		json.RawMessage(activity.InputJSON)) &&
		saved.Call.Name == toolNameLaunchAgent &&
		saved.Call.ID == parentToolCallIDFromContext(ctx)
}

func (n *nativeWorkflowRunner) prepareChildActor(
	ctx context.Context,
	deps *launchAgentDeps,
	launch preparedChildLaunch,
	task string,
	run *AgentRun,
	sink *agentRunSink,
) (*childWorkflowActor, error) {
	runtime := n.dispatch.runtime
	if err := runtime.openChildAgentRun(ctx, deps, launch, run, sink,
		task); err != nil {
		return nil, ctxerrors.Wrap(err, "open native child transcript")
	}

	prepared, err := runtime.prepareChildConversation(ctx, deps, launch,
		task, run)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare native child conversation")
	}

	return &childWorkflowActor{
		run: run, deps: deps, prepared: prepared,
		sink: newAgentRunSink(runtime.store, run.SessionID, run,
			n.dispatch.child.sink, deps.modelReference),
	}, nil
}

func (n *nativeWorkflowRunner) submitChild(
	ctx context.Context,
	parentID uuid.UUID,
	actor *childWorkflowActor,
	launch preparedChildLaunch,
) (*models.Execution, error) {
	conversation, err := n.dispatch.runtime.childWorkflowConversation(ctx,
		actor.deps, launch, actor.run, actor.prepared)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "snapshot submitted child context")
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode submitted child context")
	}

	run := actor.run
	executionID := uuid.NewSHA1(run.ID, []byte(childWorkflowIdentity))

	execution, err := n.engine.options.Store.CreateExecution(ctx,
		run.SessionID, session.CreateExecutionInput{
			ID: executionID,
			WorkflowExecutionID: nativeWorkflowInstance(run.SessionID,
				executionID).ExecutionID,
			RootTurnID: run.ParentTurnID, ParentExecutionID: &parentID,
			AgentRunID: &run.ID, WorkflowVersion: agentWorkflowName,
			Task:             conversation.Child.Task,
			WorkingDirectory: actor.deps.executor.Workspace(), Context: encoded,
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "record submitted child execution")
	}

	if err := n.bindSubmittedChild(ctx, execution, actor); err != nil {
		return execution, ctxerrors.Wrap(err, "bind submitted child callbacks")
	}

	scheduleErr := n.submitRetainedExecution(ctx, execution)
	if scheduleErr != nil {
		return execution, ctxerrors.Wrap(scheduleErr,
			"schedule submitted child workflow")
	}

	return execution, nil
}

func (n *nativeWorkflowRunner) reconcileSubmittedChild(
	ctx context.Context,
	execution *models.Execution,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		workflowPauseTimeout)
	defer cancel()

	current, err := n.engine.options.Store.GetExecution(cleanup,
		execution.SessionID, execution.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "recheck submitted child state")
	}

	if current.State != models.ExecutionStateCancelled {
		return nil
	}

	return n.cancelChildExecution(cleanup, current)
}

func (n *nativeWorkflowRunner) bindSubmittedChild(
	ctx context.Context,
	execution *models.Execution,
	actor *childWorkflowActor,
) error {
	if execution.State == models.ExecutionStateCompleted ||
		execution.State == models.ExecutionStateFailed ||
		execution.State == models.ExecutionStateCancelled {
		return nil
	}

	if execution.State == models.ExecutionStatePaused {
		actor.run.isPaused.Store(true)
	}

	slot := n.dispatch.child.slot(execution.ID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit submitted child callbacks")
	}
	defer release()

	n.dispatch.runtime.childMessageMutex.Lock()
	defer n.dispatch.runtime.childMessageMutex.Unlock()

	if slot.current != nil {
		if slot.current.actor == actor {
			return nil
		}

		return ctxerrors.Wrap(commerr.ErrConflict,
			"submitted child already has a callback owner")
	}

	if err := actor.run.inbox.bindWorkflow(ctx, execution.ID,
		actor.deps.executor.Workspace()); err != nil {
		return ctxerrors.Wrap(err, "bind submitted child durable inbox")
	}

	slot.current = &childCallbackScope{
		actor: actor, revision: execution.ControlRevision, context: ctx,
		cancel: n.cancelChildOnWorkerStop(actor.run),
	}

	return nil
}

func (n *nativeWorkflowRunner) cancelChildOnWorkerStop(
	run *AgentRun,
) context.CancelFunc {
	stop := context.AfterFunc(n.engine.options.Lifecycle, run.cancel)

	return func() { stop(); run.cancel() }
}
