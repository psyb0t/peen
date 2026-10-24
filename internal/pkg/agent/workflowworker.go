package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	workflowworker "github.com/psyb0t/go-workflows/worker"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// NewWorkflowWorker assembles native session tasks without starting them.
// Cancel the startup context and drain tasks before closing storage.
func NewWorkflowWorker(
	ctx context.Context,
	runtime *Runtime,
	storage backend.Backend,
	generationID, sessionID uuid.UUID,
	sink EventSink,
) (*workflowworker.Worker, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "assemble native workflow worker")
	}

	if runtime == nil || storage == nil || generationID == uuid.Nil ||
		sessionID == uuid.Nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native workflow worker dependencies")
	}

	store, ok := runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"native workflow worker execution storage")
	}

	options := workflowExecutionOptions{
		Store: store, GenerationID: generationID, Lifecycle: ctx,
		MergeRound: workflowMergeRound,
	}
	dispatch := newWorkflowDispatch(ctx, runtime, generationID, sink)
	dispatch.bind(&options)
	engine := &workflowExecution{options: options}
	workerOptions := workflowworker.DefaultOptions
	workerOptions.WorkflowExecutorCacheTTL = workflowExecutorCacheTTL
	queue := workflow.Queue(sessionID.String())
	workerOptions.WorkflowQueues = []workflow.Queue{queue}
	workerOptions.ActivityQueues = []workflow.Queue{queue}

	worker := workflowworker.New(storage, &workerOptions)
	if err := engine.register(worker); err != nil {
		return nil, ctxerrors.Wrap(err, "register native workflow worker")
	}

	if err := runtime.bindWorkflowRunner(storage, dispatch, engine,
		sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "bind native workflow runner")
	}

	if err := runtime.nativeWorkflowRunner().restoreWorkflowControls(
		ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "restore native workflow controls")
	}

	return worker, nil
}
