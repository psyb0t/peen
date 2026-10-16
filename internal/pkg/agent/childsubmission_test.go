package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	nativeChildInstructions      = "native child routing marker"
	nativeGrandchildInstructions = "native grandchild routing marker"
)

type nativeTreeDriver struct {
	*elelemtest.ScriptedDriver
	routes       map[string]*elelemtest.ScriptedDriver
	beforeStream func(context.Context, string) error
}

func (d nativeTreeDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	callback func(elelem.Delta) error,
) (elelem.Usage, error) {
	prompt := request.Messages[0].Text()
	for route, driver := range d.routes {
		if !strings.Contains(prompt, route) {
			continue
		}

		if d.beforeStream != nil {
			if err := d.beforeStream(ctx, route); err != nil {
				return elelem.Usage{}, ctxerrors.Wrap(err,
					"wait for parallel child providers")
			}
		}

		return driver.Stream(ctx, request, callback)
	}

	return d.ScriptedDriver.Stream(ctx, request, callback)
}

func nativeTreeResponses(first elelemtest.Turn, text string) []elelemtest.Turn {
	turns := make([]elelemtest.Turn, 1, 9)
	turns[0] = first
	for range 8 {
		turns = append(turns, elelemtest.Text(text))
	}

	return turns
}

func nativeLaunchArguments(t *testing.T, name, instructions string) string {
	t.Helper()
	encoded, err := json.Marshal(launchAgentInput{
		Task: "inspect nested work",
		AgentDefinition: &agentDefinitionInput{
			Name: name, Instructions: instructions,
		},
	})
	require.NoError(t, err)

	return string(encoded)
}

func TestNativeChildSubmissionRunsGrandchildWithDurableLineage(t *testing.T) {
	t.Parallel()
	driver := nativeTreeDriver{
		ScriptedDriver: elelemtest.NewScriptedDriver(nativeTreeResponses(
			elelemtest.ToolCall("launchNativeChild", toolNameLaunchAgent,
				nativeLaunchArguments(t, "nativeChild", nativeChildInstructions)),
			"root result")...),
		routes: map[string]*elelemtest.ScriptedDriver{
			nativeChildInstructions: elelemtest.NewScriptedDriver(
				nativeTreeResponses(elelemtest.ToolCall("launchNativeGrandchild",
					toolNameLaunchAgent, nativeLaunchArguments(t, "nativeGrandchild",
						nativeGrandchildInstructions)), "child result")...),
			nativeGrandchildInstructions: elelemtest.NewScriptedDriver(
				elelemtest.Text("nested result")),
		},
	}
	base := newNativeRunnerFixture(t, driver)
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		Message: "launch nested work",
	})
	require.NoError(t, err)
	assert.Equal(t, "root result", result.Text)
	require.Eventually(t, func() bool {
		page, readErr := base.store.SearchExecutions(t.Context(),
			base.runtime.sessionID, session.SearchExecutionsOptions{})
		if readErr != nil || len(page.Items) != 3 {
			return false
		}

		for _, item := range page.Items {
			if item.State != models.ExecutionStateCompleted {
				return false
			}
		}

		return true
	}, 20*time.Second, 10*time.Millisecond)
	assertNativeTreeLineage(t, base)
}

func nativeTreeRoute(name string) string {
	return fmt.Sprintf("<nativeTreeRoute>%s</nativeTreeRoute>", name)
}

func nativeTreeLaunches(t *testing.T, names []string) elelemtest.Turn {
	t.Helper()
	if len(names) == 0 {
		return elelemtest.Text("tree leaf finished")
	}

	deltas := make([]elelem.Delta, len(names))
	for index, name := range names {
		deltas[index].ToolCall = &elelem.ToolCallDelta{
			Index: index, ID: "launch" + name, Name: toolNameLaunchAgent,
			Arguments: nativeLaunchArguments(t, name, nativeTreeRoute(name)),
		}
	}

	deltas[len(deltas)-1].FinishReason = elelem.FinishReasonToolCalls

	return elelemtest.Turn{Deltas: deltas}
}

func nativeParallelTreeDriver(t *testing.T) nativeTreeDriver {
	t.Helper()
	children := map[string][]string{
		"branchA": {"leafA", "leafB", "leafC"},
		"branchB": {"leafD"}, "branchC": nil,
		"leafA": nil, "leafB": nil, "leafC": nil, "leafD": nil,
	}
	driver := nativeTreeDriver{
		ScriptedDriver: elelemtest.NewScriptedDriver(nativeTreeResponses(
			nativeTreeLaunches(t, []string{"branchA", "branchB", "branchC"}),
			"tree root finished")...),
		routes: make(map[string]*elelemtest.ScriptedDriver, len(children)),
	}
	for name, names := range children {
		driver.routes[nativeTreeRoute(name)] = elelemtest.NewScriptedDriver(
			nativeTreeResponses(nativeTreeLaunches(t, names),
				"tree branch finished")...)
	}

	return driver
}

func TestNativeChildSubmissionRunsEightAgentTreeInParallel(t *testing.T) {
	t.Parallel()
	driver := nativeParallelTreeDriver(t)
	started := make(chan string, 3)
	release := make(chan struct{})
	allow := sync.OnceFunc(func() { close(release) })
	t.Cleanup(allow)
	driver.beforeStream = parallelTreeBarrier(started, release)
	base := newNativeRunnerFixtureWithOptions(t, driver,
		func(options *RuntimeOptions) {
			options.AgentLimits.MaxConcurrentRuns = 8
		})
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		Message: "launch three independent branches",
	})
	require.NoError(t, err)
	assert.Equal(t, "tree root finished", result.Text)
	observed := make(map[string]bool)
	for range 3 {
		select {
		case name := <-started:
			observed[name] = true
		case <-time.After(10 * time.Second):
			t.Fatal("three child providers did not run concurrently")
		}
	}
	assert.Len(t, observed, 3)
	allow()
	assertEightAgentExecutionTree(t, base)
}

func parallelTreeBarrier(
	started chan<- string,
	release <-chan struct{},
) func(context.Context, string) error {
	var mutex sync.Mutex

	seen := make(map[string]bool)

	return func(ctx context.Context, route string) error {
		if route != nativeTreeRoute("branchA") &&
			route != nativeTreeRoute("branchB") &&
			route != nativeTreeRoute("branchC") {
			return nil
		}

		mutex.Lock()
		first := !seen[route]
		seen[route] = true
		mutex.Unlock()
		if first {
			started <- route
		}

		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "parallel tree provider cancelled")
		}
	}
}

func assertEightAgentExecutionTree(t *testing.T, base runtimeFixture) {
	t.Helper()
	var items []*models.Execution

	require.Eventually(t, func() bool {
		page, err := base.store.SearchExecutions(t.Context(),
			base.runtime.sessionID, session.SearchExecutionsOptions{})
		if err != nil || len(page.Items) != 8 {
			return false
		}

		items = page.Items
		for _, item := range items {
			if item.State != models.ExecutionStateCompleted {
				return false
			}
		}

		return true
	}, 30*time.Second, 10*time.Millisecond)
	for _, item := range items {
		if item.Kind == models.ExecutionKindRoot {
			assert.Nil(t, item.ParentExecutionID)

			continue
		}

		require.NotNil(t, item.ParentExecutionID)
		parent, err := base.store.GetExecution(t.Context(), item.SessionID,
			*item.ParentExecutionID)
		require.NoError(t, err)
		assert.Equal(t, parent.RootExecutionID, item.RootExecutionID)
		run, err := base.store.GetAgentRun(t.Context(), item.SessionID,
			*item.AgentRunID)
		require.NoError(t, err)
		assert.Equal(t, parent.AgentRunID, run.ParentAgentRunID)
		assert.Equal(t, models.AgentRunStateCompleted, run.State)
	}
}

func assertNativeTreeLineage(t *testing.T, base runtimeFixture) {
	t.Helper()
	registry, err := base.runtime.sessionAgentRuns(base.runtime.sessionID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, run := range registry.List() {
			if run.Snapshot().State != AgentRunStateCompleted {
				return false
			}
		}

		return true
	}, 10*time.Second, 10*time.Millisecond)
	runs := registry.List()
	require.Len(t, runs, 2)
	for _, run := range runs {
		assert.Equal(t, AgentRunStateCompleted, run.Snapshot().State)
		if run.Depth == 1 {
			assert.Nil(t, run.ParentAgentRunID)
			assert.Equal(t, "launchNativeChild", run.ParentToolCallID)

			continue
		}

		assert.Equal(t, 2, run.Depth)
		require.NotNil(t, run.ParentAgentRunID)
		parent, found := registry.Get(*run.ParentAgentRunID)
		require.True(t, found)
		assert.Equal(t, 1, parent.Depth)
		assert.Equal(t, "launchNativeGrandchild", run.ParentToolCallID)
	}
}

func nativeChildParentFixture(t *testing.T) (
	executionLoopFixture, *nativeWorkflowRunner, *launchAgentDeps,
	context.Context, session.ActivityLease,
) {
	t.Helper()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	call := workflowToolInput{
		Round: workflowRoundInput{Execution: fixture.input},
		Call:  elelem.ToolCall{ID: "acceptedChildCall", Name: toolNameLaunchAgent},
	}
	encoded, err := json.Marshal(call)
	require.NoError(t, err)
	claim, err := fixture.base.store.BeginExecutionActivity(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		fixture.engine.options.GenerationID, session.ScheduleActivityInput{
			StepID: "tool:0:0", Kind: models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Name:         toolActivityName, Input: encoded,
		})
	require.NoError(t, err)
	native := &nativeWorkflowRunner{
		engine: fixture.engine,
		dispatch: newWorkflowDispatch(fixture.engine.options.Lifecycle,
			fixture.base.runtime, fixture.engine.options.GenerationID, nil),
	}
	ctx := contextWithParentToolCallID(t.Context(), call.Call.ID)

	return fixture, native, fixture.base.runtime.launchAgentDeps(prepared),
		ctx, claim.Lease
}

func TestNativeChildSubmissionRequiresLiveParentClaim(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                                    string
		missing, missingDeps, paused, completed bool
		alter                                   func(context.Context, *launchAgentDeps,
			*session.ActivityLease) context.Context
		wantErr error
	}{
		{name: "accepted"},
		{name: "missing", missing: true, wantErr: commerr.ErrPermissionDenied},
		{
			name: "missing dependencies", missingDeps: true,
			wantErr: commerr.ErrPermissionDenied,
		},
		{name: "paused", paused: true, wantErr: commerr.ErrConflict},
		{name: "completed claim", completed: true, wantErr: commerr.ErrConflict},
		{
			name: "replaced generation", wantErr: commerr.ErrPermissionDenied,
			alter: func(ctx context.Context, _ *launchAgentDeps,
				lease *session.ActivityLease,
			) context.Context {
				lease.GenerationID = uuid.New()

				return ctx
			},
		},
		{
			name: "stale attempt", wantErr: commerr.ErrConflict,
			alter: func(ctx context.Context, _ *launchAgentDeps,
				lease *session.ActivityLease,
			) context.Context {
				lease.Attempt++

				return ctx
			},
		},
		{
			name: "foreign session", wantErr: commerr.ErrPermissionDenied,
			alter: func(ctx context.Context, deps *launchAgentDeps,
				_ *session.ActivityLease,
			) context.Context {
				deps.sessionID = uuid.New()

				return ctx
			},
		},
		{
			name: "foreign root turn", wantErr: commerr.ErrConflict,
			alter: func(ctx context.Context, deps *launchAgentDeps,
				_ *session.ActivityLease,
			) context.Context {
				deps.parentTurnID = uuid.New()

				return ctx
			},
		},
		{
			name: "foreign parent run", wantErr: commerr.ErrConflict,
			alter: func(ctx context.Context, _ *launchAgentDeps,
				_ *session.ActivityLease,
			) context.Context {
				return contextWithParentAgentRun(ctx, &AgentRun{ID: uuid.New()})
			},
		},
		{
			name: "different call", wantErr: commerr.ErrConflict,
			alter: func(ctx context.Context, _ *launchAgentDeps,
				_ *session.ActivityLease,
			) context.Context {
				return contextWithParentToolCallID(ctx, "differentCall")
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, native, deps, ctx, lease := nativeChildParentFixture(t)
			if tc.paused {
				_, err := fixture.base.store.PauseExecution(ctx,
					fixture.input.SessionID, fixture.input.ExecutionID, "retain work")
				require.NoError(t, err)
			}
			if tc.completed {
				_, err := fixture.base.store.CompleteExecutionActivity(ctx,
					fixture.input.SessionID, lease,
					session.CompleteActivityInput{
						Output: json.RawMessage(`{}`), Context: fixture.input.Context,
					})
				require.NoError(t, err)
			}
			if tc.alter != nil {
				ctx = tc.alter(ctx, deps, &lease)
			}
			if !tc.missing {
				ctx = context.WithValue(ctx, workflowActivityLeaseContextKey{}, lease)
			}
			if tc.missingDeps {
				deps = nil
			}

			parent, err := native.childSubmissionParent(ctx, deps)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				require.NotNil(t, parent)
				assert.Equal(t, fixture.input.ExecutionID, parent.ID)
			}

			page, err := fixture.base.store.SearchExecutions(t.Context(),
				fixture.input.SessionID, session.SearchExecutionsOptions{})
			require.NoError(t, err)
			assert.Len(t, page.Items, 1)
		})
	}
}

type rejectingChildExecutionStore struct {
	session.ExecutionStorage
}

func (s rejectingChildExecutionStore) CreateExecution(
	_ context.Context,
	_ uuid.UUID,
	_ session.CreateExecutionInput,
) (*models.Execution, error) {
	return nil, ctxerrors.Wrap(commerr.ErrConflict,
		"reject child execution creation")
}

func TestNativeChildSubmissionCreationFailureReleasesRun(t *testing.T) {
	t.Parallel()
	fixture, native, deps, ctx, lease := nativeChildParentFixture(t)
	ctx = context.WithValue(ctx, workflowActivityLeaseContextKey{}, lease)
	input := launchAgentInput{
		Task: "inspect work",
		AgentDefinition: &agentDefinitionInput{
			Name: "rejectedChild", Instructions: "inspect sources",
		},
	}
	launch, err := fixture.base.runtime.prepareChildLaunch(ctx, deps, input)
	require.NoError(t, err)
	native.engine.options.Store = rejectingChildExecutionStore{
		ExecutionStorage: fixture.base.store,
	}
	_, err = native.launchChild(ctx, deps, input, launch, input.Task,
		fixture.base.runtime.messageSender(ctx))
	require.ErrorIs(t, err, commerr.ErrConflict)
	runs := launch.registry.List()
	require.Len(t, runs, 1)
	assert.Equal(t, AgentRunStateFailed, runs[0].Snapshot().State)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		fixture.input.SessionID, runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateFailed, stored.State)
	assert.NotNil(t, stored.EndedAt)
	page, err := fixture.base.store.SearchExecutions(t.Context(),
		fixture.input.SessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestNativeChildSubmissionRetryKeepsPausedIdentity(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	run.cancel = cancel
	sink := newAgentRunSink(fixture.base.store, deps.sessionID, run, nil,
		deps.modelReference)
	inbox, err := newAgentInbox(sink, fixture.base.runtime.maxQueuedUserMessages)
	require.NoError(t, err)
	run.inbox = inbox
	actor := &childWorkflowActor{
		run: run, sink: sink, deps: deps, prepared: prepared,
	}
	native := &nativeWorkflowRunner{
		engine: fixture.engine, api: newWorkflowClient(fixture.storage),
		dispatch: newWorkflowDispatch(fixture.engine.options.Lifecycle,
			fixture.base.runtime, fixture.engine.options.GenerationID, nil),
	}
	first, err := native.submitChild(ctx, fixture.input.ExecutionID,
		actor, launch)
	require.NoError(t, err)
	_, err = fixture.base.store.PauseExecution(ctx, deps.sessionID, first.ID,
		"leave child paused")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(ctx, deps.sessionID, first.ID)
	require.NoError(t, err)
	instance, err := native.api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", deps.sessionID, first.ID),
		}, agentWorkflowName, workflowExecutionInput{})
	require.NoError(t, err)
	history, err := fixture.storage.GetWorkflowInstanceHistory(ctx, instance, nil)
	require.NoError(t, err)
	repeated, err := native.submitChild(ctx, fixture.input.ExecutionID,
		actor, launch)
	require.NoError(t, err)
	assert.Equal(t, first.ID, repeated.ID)
	assert.Equal(t, before.State, repeated.State)
	assert.Equal(t, before.ControlRevision, repeated.ControlRevision)
	assert.Equal(t, agentWorkflowName, repeated.WorkflowVersion)
	after, err := fixture.storage.GetWorkflowInstanceHistory(ctx, instance, nil)
	require.NoError(t, err)
	assert.Equal(t, history, after)
	page, err := fixture.base.store.SearchExecutions(ctx, deps.sessionID,
		session.SearchExecutionsOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

func TestNativeChildSubmissionRecoversSavedUnscheduledChild(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t, elelemtest.Text("recovered child result"))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	run.cancel = cancel
	sink := newAgentRunSink(fixture.base.store, deps.sessionID, run, nil, deps.modelReference)
	inbox, err := newAgentInbox(sink, fixture.base.runtime.maxQueuedUserMessages)
	require.NoError(t, err)
	run.inbox = inbox
	actor := &childWorkflowActor{run: run, sink: sink, deps: deps, prepared: prepared}
	native := &nativeWorkflowRunner{
		engine:   fixture.engine,
		api:      newWorkflowClient(rootSubmissionTimeoutBackend{Backend: fixture.storage}),
		dispatch: newWorkflowDispatch(fixture.engine.options.Lifecycle, fixture.base.runtime, fixture.engine.options.GenerationID, nil),
	}
	native.dispatch.bind(&fixture.engine.options)
	saved, err := native.submitChild(ctx, fixture.input.ExecutionID, actor, launch)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, saved)
	original, err := retainedWorkflowSubmission(saved)
	require.NoError(t, err)
	assert.Equal(t, prepared.remainingRounds, original.MaxRounds)
	assert.Equal(t, fixture.base.runtime.maxConcurrentTools, original.MaxConcurrentTools)
	fixture.base.runtime.maxToolRounds = 1
	fixture.base.runtime.maxConcurrentTools = 1
	native.api = newWorkflowClient(fixture.storage)
	native.dispatch = newWorkflowDispatch(fixture.engine.options.Lifecycle, fixture.base.runtime, fixture.engine.options.GenerationID, nil)
	native.dispatch.bind(&fixture.engine.options)
	require.NoError(t, native.restoreWorkflowControls(ctx))
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)
	instance := nativeWorkflowInstance(saved.SessionID, saved.ID)
	result, err := client.GetWorkflowResult[workflowRoundResult](ctx, native.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "recovered child result", result.Text)
	stored, err := fixture.base.store.GetExecution(ctx, saved.SessionID, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, stored.State)
	assert.Equal(t, saved.AgentRunID, stored.AgentRunID)
	assert.Equal(t, saved.ParentExecutionID, stored.ParentExecutionID)
}
