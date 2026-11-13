package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rolloverTestLeaseTimeout = 100 * time.Millisecond
	rolloverTestRestartCap   = 200
)

type rolloverCrashBackend struct {
	backend.Backend
	before  bool
	entered chan struct{}
	killed  chan struct{}
	once    sync.Once
}

func TestWorkflowRolloverStopAndNudge(t *testing.T) {
	t.Parallel()
	fixture, dispatch := newRolloverRootFixture(t,
		rolloverTestTurns(t, rolloverTestRounds))
	fixture.input.HistoryThreshold = rolloverTestThreshold
	storage, original := prepareRolloverTest(t, fixture)
	entered := make(chan struct{})
	var blocked atomic.Bool
	prepare := fixture.engine.options.PrepareRound
	fixture.engine.options.PrepareRound = func(ctx context.Context,
		input workflowRoundInput,
	) (json.RawMessage, error) {
		if input.Execution.NextRound > 0 && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-ctx.Done()

			return nil, ctxerrors.Wrap(ctx.Err(), "stop continued preparation")
		}

		return prepare(ctx, input)
	}
	api, _ := startRolloverTestWorker(t, fixture, storage)
	require.NoError(t, fixture.base.runtime.bindWorkflowRunner(storage,
		dispatch, fixture.engine, fixture.input.SessionID))
	native := fixture.base.runtime.nativeWorkflowRunner()
	_, err := api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: original.InstanceID,
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		},
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("continued preparation did not start")
	}
	stopped, err := fixture.base.runtime.PauseSessionWorkflows(t.Context())
	require.NoError(t, err)
	assert.True(t, stopped)
	saved, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, saved.State)
	assert.NotEqual(t, original.ExecutionID, saved.WorkflowExecutionID)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		saved.SessionID, saved.ID)
	require.NoError(t, err)
	require.NoError(t, native.nudgeExecution(t.Context(), saved))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, original, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
}

func TestWorkflowRolloverChildCancellation(t *testing.T) {
	t.Parallel()
	fixture, dispatch := newRolloverChildFixture(t,
		rolloverTestTurns(t, rolloverTestRounds))
	fixture.input.HistoryThreshold = rolloverTestThreshold
	storage, original := prepareRolloverTest(t, fixture)
	entered := make(chan struct{})
	var once sync.Once
	prepare := fixture.engine.options.PrepareRound
	fixture.engine.options.PrepareRound = func(ctx context.Context,
		input workflowRoundInput,
	) (json.RawMessage, error) {
		if input.Execution.NextRound > 0 {
			once.Do(func() { close(entered) })
			<-ctx.Done()

			return nil, ctxerrors.Wrap(ctx.Err(), "cancel continued child")
		}

		return prepare(ctx, input)
	}
	api, _ := startRolloverTestWorker(t, fixture, storage)
	native := &nativeWorkflowRunner{
		api: api, storage: storage,
		engine: fixture.engine, dispatch: dispatch,
	}
	_, err := api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: original.InstanceID,
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		},
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("continued child did not start")
	}
	saved, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NotNil(t, saved.AgentRunID)
	_, requested, err := fixture.base.store.RequestAgentRunCancellation(
		t.Context(), saved.SessionID, *saved.AgentRunID)
	require.NoError(t, err)
	assert.True(t, requested)
	saved, err = fixture.base.store.GetExecution(t.Context(),
		saved.SessionID, saved.ID)
	require.NoError(t, err)
	require.NoError(t, native.cancelChildExecution(t.Context(), saved))
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, original, workflowTestResultTimeout)
	require.Error(t, err)
	saved, err = fixture.base.store.GetExecution(t.Context(),
		saved.SessionID, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCancelled, saved.State)
	current, err := db.ResolveWorkflowExecution(t.Context(), storage, original)
	require.NoError(t, err)
	assert.NotEqual(t, original.ExecutionID, current.ExecutionID)
	state, err := storage.GetWorkflowInstanceState(t.Context(), current)
	require.NoError(t, err)
	assert.Equal(t, core.WorkflowInstanceStateFinished, state)
}

func (b *rolloverCrashBackend) CompleteWorkflowTask(ctx context.Context,
	task *backend.WorkflowTask, state core.WorkflowInstanceState,
	executed, activities, timers []*history.Event,
	targets []*history.WorkflowEvent,
) error {
	if state != core.WorkflowInstanceStateContinuedAsNew {
		return b.Backend.CompleteWorkflowTask(ctx, task, state,
			executed, activities, timers, targets)
	}

	if !b.before {
		if err := b.Backend.CompleteWorkflowTask(ctx, task, state,
			executed, activities, timers, targets); err != nil {
			return ctxerrors.Wrap(err, "commit rollover crash fixture")
		}
	}

	b.once.Do(func() { close(b.entered) })
	<-b.killed

	return ctxerrors.Wrap(context.Canceled, "kill rollover at handoff")
}

func TestWorkflowRolloverCrashRecovery(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"before handoff", "after handoff", "after cache"} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			fixture, dispatch := newRolloverRootFixture(t,
				rolloverTestTurns(t, rolloverTestRounds))
			callbacks := dispatch.root
			fixture.input.HistoryThreshold = rolloverTestThreshold
			storage, original := prepareRolloverTest(t, fixture)
			backend.WithWorkflowLockTimeout(rolloverTestLeaseTimeout)(
				storage.Options())
			backend.WithStickyTimeout(rolloverTestLeaseTimeout)(storage.Options())
			crash := &rolloverCrashBackend{
				Backend: storage,
				before:  point == "before handoff", entered: make(chan struct{}),
				killed: make(chan struct{}),
			}
			api, stop := startRolloverTestWorker(t, fixture, crash)
			kill := sync.OnceFunc(func() { close(crash.killed) })
			t.Cleanup(kill)
			_, err := api.CreateWorkflowInstance(t.Context(),
				client.WorkflowInstanceOptions{
					InstanceID: original.InstanceID,
					Queue:      workflow.Queue(fixture.input.SessionID.String()),
				},
				agentWorkflowName, fixture.input)
			require.NoError(t, err)
			select {
			case <-crash.entered:
			case <-time.After(workflowTestResultTimeout):
				t.Fatal("workflow did not reach crash boundary")
			}

			repo := repositories.Use(fixture.base.handle.GormDB).Execution
			saved, err := repo.WithContext(t.Context()).Where(
				repo.ID.Eq(fixture.input.ExecutionID)).First()
			require.NoError(t, err)
			assert.Equal(t, original.ExecutionID, saved.WorkflowExecutionID)
			resolved, err := db.ResolveWorkflowExecution(t.Context(),
				storage, original)
			require.NoError(t, err)
			if crash.before {
				assert.Equal(t, original.ExecutionID, resolved.ExecutionID)
			} else {
				assert.NotEqual(t, original.ExecutionID, resolved.ExecutionID)
			}

			if point == "after cache" {
				saved, err = fixture.base.store.GetExecution(t.Context(),
					saved.SessionID, saved.ID)
				require.NoError(t, err)
				assert.Equal(t, resolved.ExecutionID, saved.WorkflowExecutionID)
			}
			stats, err := fixture.storage.GetStats(t.Context())
			require.NoError(t, err)
			assert.EqualValues(t, 1, stats.ActiveWorkflowInstances)

			kill()
			stop()
			shared := callbacks.slot(fixture.input.ExecutionID).current
			if shared != nil {
				fixture.base.runtime.closeActiveUserMessageQueue(shared.prepared)
			}
			require.NoError(t, fixture.base.handle.Close())
			reopened, err := db.Open(t.Context(),
				db.Config{Directory: fixture.base.stateDirectory})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			workflows, err := db.OpenWorkflowStorage(t.Context(), db.Config{
				Directory:    fixture.base.stateDirectory,
				ContentBlobs: reopened.ContentBlobs(),
			})
			require.NoError(t, err)
			reopened.Workflows = workflows
			store, err := session.NewStore(reopened, session.Options{})
			require.NoError(t, err)
			fixture.base.handle = reopened
			fixture.base.store = store
			fixture.base.runtime.store = store
			fixture.base.runtime.resumeAfterCrash = true
			resumed, err := workflows.WorkerBackend(t.Context(), uuid.New())
			require.NoError(t, err)
			backend.WithMaxHistorySize(rolloverTestRestartCap)(resumed.Options())
			resumed.Options().Converter = contentblob.NewConverter(store)
			workerContext, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			worker, err := NewWorkflowWorker(workerContext, fixture.base.runtime,
				resumed, uuid.New(), fixture.input.SessionID, nil)
			require.NoError(t, err)
			require.NoError(t, worker.Start(workerContext))
			t.Cleanup(func() {
				cancel()
				require.NoError(t, worker.WaitForCompletion())
			})
			api = fixture.base.runtime.nativeWorkflowRunner().api
			result, err := client.GetWorkflowResult[workflowRoundResult](
				t.Context(), api, original, workflowTestResultTimeout)
			require.NoError(t, err)
			assert.Equal(t, runtimeToolFinalText, result.Text)
			assertRolloverTestHistory(t, fixture, resumed, original, result,
				rolloverTestRounds)
			stats, err = workflows.GetStats(t.Context())
			require.NoError(t, err)
			assert.Zero(t, stats.ActiveWorkflowInstances)
		})
	}
}
