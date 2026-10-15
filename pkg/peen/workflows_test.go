package peen

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const embeddingWorkflowCompletionTimeout = time.Minute

type embeddingStartupReadFailureStore struct {
	*session.Store
}

func (s embeddingStartupReadFailureStore) SearchExecutions(
	context.Context, uuid.UUID, session.SearchExecutionsOptions,
) (*session.ExecutionPage, error) {
	return nil, ctxerrors.Wrap(commerr.ErrFetchFailed,
		"inject startup execution read failure")
}

func TestEmbeddedStartupFailureClosesOwnedResources(t *testing.T) {
	t.Parallel()
	options := workflowEmbeddingOptions(t, elelemtest.NewScriptedDriver()).withDefaults()
	options.InMemoryStore = true
	options.MetricsRegisterer = prometheus.NewRegistry()
	models, err := resolveEmbeddingModels(options)
	require.NoError(t, err)
	hub := newEventHub(options.EventLimits.SubscriberBuffer)
	configuration, err := configureEmbedding(options, options.ConfigDirectory,
		options.Workspace, options.StateDirectory, false, models, nil, nil, hub)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, configuration.telemetry.close())
		require.NoError(t, removeTemporaryState(configuration.temporaryState))
	})
	assembled, err := agent.Assemble(t.Context(), embeddingAssembly(options, configuration, nil))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, assembled.Handle.Close()) })
	resolver, err := harness.NewResolver(options.ConfigDirectory, harness.Limits{})
	require.NoError(t, err)
	runtimeOptions := configuration.runtime
	runtimeOptions.Store = embeddingStartupReadFailureStore{assembled.Store}
	runtimeOptions.Resolver = resolver
	assembled.Runtime, err = agent.NewRuntime(t.Context(), runtimeOptions)
	require.NoError(t, err)

	runtime, err := newEmbeddedRuntime(assembled, configuration, options, hub)
	require.ErrorIs(t, err, commerr.ErrFetchFailed)
	assert.Nil(t, runtime)
	assert.Error(t, assembled.Handle.SQLDB().PingContext(t.Context()))
	assert.NoDirExists(t, configuration.temporaryState)
	assert.True(t, hub.closed)
	replacement, err := newTelemetry(options, options.StateDirectory)
	require.NoError(t, err, "startup failure must release registered metrics")
	require.NoError(t, replacement.close())
}

func TestEmbeddedStartupAbortCancelsAndDrainsStartedSessions(t *testing.T) {
	t.Parallel()
	runtime, err := New(workflowEmbeddingOptions(t, elelemtest.NewScriptedDriver()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	second, err := runtime.OpenSession(t.Context(), OpenSessionOptions{Workspace: t.TempDir()})
	require.NoError(t, err)
	for _, core := range []*agent.Runtime{runtime.internal, second.core()} {
		operation, finish, err := core.BeginOperation(t.Context())
		require.NoError(t, err)
		t.Cleanup(finish)
		go func() {
			<-operation.Done()
			finish()
		}()
	}
	done := make(chan error, 1)
	go func() { done <- runtime.abortStartup() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Error("startup cleanup did not cancel admitted work")
		// Release fixture operations so a failed assertion can still drain.
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, runtime.internal.Shutdown(cancelled), context.Canceled)
		require.ErrorIs(t, second.core().Shutdown(cancelled), context.Canceled)
		require.NoError(t, <-done)
	}
	for _, entry := range runtime.workflows.sessions {
		select {
		case <-entry.done:
		default:
			t.Error("startup cleanup returned before a worker drained")
		}
	}
	_, err = runtime.handle.Workflows.WorkerBackend(t.Context(), uuid.New())
	require.Error(t, err)
	assert.Error(t, runtime.handle.SQLDB().PingContext(t.Context()))
}

func workflowEmbeddingOptions(t *testing.T, driver elelem.Driver) Options {
	t.Helper()
	config := t.TempDir()
	directory := filepath.Join(config, ".agents", "agents")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "default.md"),
		[]byte("---\nname: default\ndescription: test agent\n---\nFollow the task."), 0o600))

	return Options{
		ConfigDirectory: config, Workspace: t.TempDir(), StateDirectory: t.TempDir(),
		RootAgent: "default", DefaultModel: "example/mock", MaxContextTokens: 8192,
		Models: map[string]ModelClient{"example/mock": {
			Client: elelem.New(driver), Model: elelem.Model{ID: "mock"},
		}},
	}
}

func TestEmbeddedSessionsUseDurableWorkflowExecutions(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("default result"), elelemtest.Text("first result"),
		elelemtest.Text("second result"))
	runtime, err := New(workflowEmbeddingOptions(t, driver))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	sessions := make([]*Session, 1, 3)
	sessions[0] = runtime.Session(runtime.internal.SessionID().String())
	for range 2 {
		opened, err := runtime.OpenSession(t.Context(), OpenSessionOptions{Workspace: t.TempDir()})
		require.NoError(t, err)
		sessions = append(sessions, opened)
	}
	for _, current := range sessions {
		_, err := current.Send(t.Context(), MessageRequest{Message: "finish this task"}, nil)
		require.NoError(t, err)
		page, err := runtime.store.SearchExecutions(t.Context(), current.id, session.SearchExecutionsOptions{})
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		saved := page.Items[0]
		assert.Equal(t, models.ExecutionKindRoot, saved.Kind)
		assert.Equal(t, models.ExecutionStateCompleted, saved.State)
		assert.NotEmpty(t, saved.WorkflowExecutionID)
		activities, err := runtime.store.ListExecutionActivities(t.Context(), current.id, saved.ID, session.ListEventsOptions{})
		require.NoError(t, err)
		assert.NotEmpty(t, activities.Items)
		for _, activity := range activities.Items {
			assert.Equal(t, models.ActivityStateCompleted, activity.State)
		}
	}
	assert.Equal(t, 3, driver.Calls())
}

func embeddingBlockingTool(entered chan struct{}, effects *atomic.Int32,
	release <-chan struct{},
) Tool {
	return Tool{
		Name: "embeddingBlock", Description: "Wait for a fixture signal.",
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ ToolInput) (ToolResult, error) {
			effects.Add(1)
			close(entered)
			select {
			case <-release:
				return ToolResult{Content: "released"}, nil
			case <-ctx.Done():
				return ToolResult{}, ctxerrors.Wrap(ctx.Err(), "cancel fixture handler")
			}
		},
	}
}

func TestEmbeddedSessionsShareTurnLimit(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("blocked", "embeddingBlock", `{}`),
		elelemtest.Text("first answer"), elelemtest.Text("second answer"))
	options := workflowEmbeddingOptions(t, driver)
	options.MaxConcurrentTurns = 1
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	var effects atomic.Int32
	options.Tools = []Tool{embeddingBlockingTool(entered, &effects, release)}
	runtime, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { unblock(); require.NoError(t, runtime.Close(context.Background())) })
	second, err := runtime.OpenSession(t.Context(), OpenSessionOptions{Workspace: t.TempDir()})
	require.NoError(t, err)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		result, err := runtime.Message(t.Context(), MessageRequest{Message: "first task"})
		if err == nil && result.Message != "first answer" {
			err = commerr.ErrInvalidState
		}
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first handler did not start")
	}
	started := make(chan struct{})
	go func() {
		close(started)
		result, err := second.Send(t.Context(), MessageRequest{Message: "second task"}, nil)
		if err == nil && result.Message != "second answer" {
			err = commerr.ErrInvalidState
		}
		secondDone <- err
	}()
	<-started
	assert.Never(t, func() bool { return driver.Calls() != 1 }, 100*time.Millisecond, 10*time.Millisecond)
	unblock()
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(embeddingWorkflowCompletionTimeout):
			t.Fatal("session did not finish")
		}
	}
	assert.EqualValues(t, 1, effects.Load())
	assert.Equal(t, 3, driver.Calls())
}

func TestEmbeddedStopRetainsPausedNonDefaultSessionAfterReopen(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("blocked", "embeddingBlock", `{}`), elelemtest.Text("unrelated answer"))
	options := workflowEmbeddingOptions(t, driver)
	entered, release := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	options.Tools = []Tool{embeddingBlockingTool(entered, &effects, release)}
	runtime, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	target, err := runtime.OpenSession(t.Context(), OpenSessionOptions{Workspace: t.TempDir()})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := target.Send(t.Context(), MessageRequest{Message: "old task"}, nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not start")
	}
	cancelled, err := target.Cancel(t.Context())
	require.NoError(t, err)
	assert.True(t, cancelled.CancelRequested)
	select {
	case err := <-done:
		require.ErrorIs(t, err, commerr.ErrCancelled)
	case <-time.After(10 * time.Second):
		t.Fatal("stopped request did not return")
	}
	require.NoError(t, runtime.Close(t.Context()))
	replacement, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replacement.Close(context.Background())) })
	page, err := replacement.store.SearchExecutions(t.Context(), target.id, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ExecutionStatePaused, page.Items[0].State)
	result, err := replacement.Session(target.ID()).Send(t.Context(), MessageRequest{Message: "different task"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "unrelated answer", result.Message)
	assert.EqualValues(t, 1, effects.Load())
	assert.Equal(t, 2, driver.Calls())
	paused, err := replacement.store.GetExecution(t.Context(), target.id, page.Items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
}

type embeddingHeldChildDriver struct {
	*elelemtest.ScriptedDriver
	started  chan struct{}
	once     sync.Once
	attempts atomic.Int32
}

func (d *embeddingHeldChildDriver) Stream(ctx context.Context,
	request elelem.DriverRequest, delta func(elelem.Delta) error,
) (elelem.Usage, error) {
	for _, message := range request.Messages {
		if message.Role != elelem.RoleUser || !strings.Contains(message.Text(), "embedding child hold") {
			continue
		}
		d.attempts.Add(1)
		d.once.Do(func() { close(d.started) })
		<-ctx.Done()

		return elelem.Usage{}, ctxerrors.Wrap(ctx.Err(), "cancel fixture child model")
	}

	return d.ScriptedDriver.Stream(ctx, request, delta)
}

func TestEmbeddedClosePausesAndDrainsChildAfterParentCompletion(t *testing.T) {
	driver := &embeddingHeldChildDriver{ScriptedDriver: elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("launch", "launchAgent", `{"agent":"default","task":"embedding child hold"}`),
		elelemtest.Text("parent finished")), started: make(chan struct{})}
	options := workflowEmbeddingOptions(t, driver)
	runtime, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	result, err := runtime.Message(t.Context(), MessageRequest{Message: "start child"})
	require.NoError(t, err)
	assert.Equal(t, "parent finished", result.Message)
	select {
	case <-driver.started:
	case <-time.After(10 * time.Second):
		t.Fatal("child model did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.NoError(t, runtime.Close(ctx))
	replacement, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replacement.Close(context.Background())) })
	page, err := replacement.store.SearchExecutions(t.Context(), replacement.internal.SessionID(), session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	var paused int
	for _, item := range page.Items {
		if item.Kind == models.ExecutionKindChild {
			assert.Equal(t, models.ExecutionStatePaused, item.State)
			paused++
		}
	}
	assert.Equal(t, 1, paused)
	assert.EqualValues(t, 1, driver.attempts.Load())
}

func TestEmbeddedWorkflowRefusesUnknownSessionWithoutCreatingIt(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("must not request"))
	runtime, err := New(workflowEmbeddingOptions(t, driver))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	before, err := runtime.store.ListSessions(t.Context(), session.ListSessionsOptions{})
	require.NoError(t, err)
	_, err = runtime.Session(uuid.NewString()).Send(t.Context(), MessageRequest{Message: "unknown session"}, nil)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	after, err := runtime.store.ListSessions(t.Context(), session.ListSessionsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Zero(t, driver.Calls())
	_, err = runtime.Session(uuid.Nil.String()).Send(t.Context(), MessageRequest{Message: "invalid session"}, nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestEmbeddedStartupRestoresEverySessionAcrossPages(t *testing.T) {
	options := workflowEmbeddingOptions(t, elelemtest.NewScriptedDriver())
	runtime, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	ids := make([]uuid.UUID, 1, session.DefaultPageLimit+2)
	ids[0] = runtime.internal.SessionID()
	for range session.DefaultPageLimit + 1 {
		opened, err := runtime.store.OpenWorkspace(t.Context(), t.TempDir(), session.OpenSessionOptions{RootAgent: options.RootAgent, ModelID: options.DefaultModel})
		require.NoError(t, err)
		ids = append(ids, opened.Session.ID)
	}
	require.NoError(t, runtime.Close(t.Context()))
	replacement, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replacement.Close(context.Background())) })
	assert.Len(t, replacement.workflows.sessions, len(ids))
	for _, id := range ids {
		current := replacement.workflows.sessions[id]
		require.NotNil(t, current)
		assert.Equal(t, id, current.runtime.SessionID())
	}
}

func TestSubmittedEmbeddedRootKeepsCapacityAfterCallerCancellation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("blocked", "embeddingBlock", `{}`),
		elelemtest.Text("original finished"), elelemtest.Text("next finished"))
	options := workflowEmbeddingOptions(t, driver)
	options.MaxConcurrentTurns = 1
	secondStarted := make(chan struct{})
	var secondID string
	var secondOnce sync.Once
	options.Hooks = map[string]HookHandler{"turnStart": func(_ context.Context, input HookInvocation) (HookOutcome, error) {
		if input.SessionID == secondID {
			secondOnce.Do(func() { close(secondStarted) })
		}

		return HookOutcome{}, nil
	}}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	var effects atomic.Int32
	options.Tools = []Tool{embeddingBlockingTool(entered, &effects, release)}
	runtime, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() { unblock(); require.NoError(t, runtime.Close(context.Background())) })
	other, err := runtime.OpenSession(t.Context(), OpenSessionOptions{Workspace: t.TempDir()})
	require.NoError(t, err)
	secondID = other.ID()
	caller, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	firstDone := make(chan error, 1)
	go func() { _, err := runtime.Message(caller, MessageRequest{Message: "original task"}); firstDone <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("original handler did not start")
	}
	cancel()
	select {
	case err := <-firstDone:
		require.ErrorIs(t, err, commerr.ErrCancelled)
	case <-time.After(10 * time.Second):
		t.Fatal("caller did not return")
	}
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := other.Send(t.Context(), MessageRequest{Message: "next task"}, nil)
		done <- err
	}()
	<-started
	assert.Never(t, func() bool {
		select {
		case <-secondStarted:
			return true
		default:
			return false
		}
	}, 500*time.Millisecond, 10*time.Millisecond)
	assert.Equal(t, 1, driver.Calls())
	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(embeddingWorkflowCompletionTimeout):
		t.Fatal("next session did not finish")
	}
	assert.EqualValues(t, 1, effects.Load())
	assert.Equal(t, 3, driver.Calls())
	page, err := runtime.store.SearchExecutions(t.Context(), runtime.internal.SessionID(), session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ExecutionStateCompleted, page.Items[0].State)
}
