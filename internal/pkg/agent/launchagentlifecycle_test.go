package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A child's own tool call can fail without failing the child's run: the
// child sees the failure as an ordinary tool result and still returns a
// final answer.
func TestLaunchAgentChildToolFailureStaysVisibleAndRunCompletes(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "read a missing file",
			Agent: launchAgentChildName,
		},
		elelemtest.ToolCall(
			"call_read",
			toolNameReadFile,
			runtimeToolArguments(t, "absent.txt"),
		),
		elelemtest.Text("the file is missing"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err, "a child tool failure must not fail the parent turn")
	assert.Equal(t, "done", result.Text)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(
		onlyDeliveredNotice(t, turnEvents).Data,
		&data,
	))
	assert.Equal(t, "the file is missing", data.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Equal(
		t,
		AgentRunStateCompleted,
		runs[0].Snapshot().State,
		"a failed child tool call must not mark the run itself failed",
	)

	runEvents, _, _ := runs[0].ReadEvents(0, 0)
	found := false

	for _, event := range runEvents {
		if event.Type != EventTypeAgentRunToolResult {
			continue
		}

		payload := toolResultPayload{}
		require.NoError(t, json.Unmarshal(event.Payload, &payload))

		if payload.IsError {
			found = true
		}
	}

	assert.True(
		t,
		found,
		"the run's own event buffer must record the failed tool call",
	)
}

// A caller cannot switch the startup workspace session by supplying a session
// ID. Child runs remain available to every later turn in that workspace.
func TestLaunchAgentRunRemainsInStartupWorkspaceSession(t *testing.T) {
	rootTurns := launchAndWaitTurns(
		t,
		launchAgentCallID,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		"done",
	)
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(append(
			rootTurns,
			elelemtest.Text("unrelated session reply"),
		)...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(
			elelemtest.Text("child says hi"),
		),
	}).after(runtimeTestAgentName, rootConversation, 2)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	registryA, err := fixture.runtime.sessionAgentRuns(first.SessionID)
	require.NoError(t, err)

	runs := registryA.List()
	require.Len(t, runs, 1)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "an unrelated session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, first.SessionID, second.SessionID)

	registryB, err := fixture.runtime.sessionAgentRuns(second.SessionID)
	require.NoError(t, err)

	got, found := registryB.Get(runs[0].ID)
	assert.True(
		t,
		found,
		"a child run from the startup workspace must remain visible",
	)
	assert.Equal(t, runs[0].ID, got.ID)
}

// finishAgentRun records a cancellation durably and reports it as the
// outcome, whatever wrapped the cancellation.
func TestFinishAgentRunRecordsACancellation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	registry, run, _, sink := startDurableAgentRun(
		context.Background(),
		t,
		fixture,
	)

	result := fixture.runtime.finishAgentRun(
		context.Background(),
		registry,
		sink,
		run,
		nil,
		&wrappedCancelError{},
	)
	assert.Equal(t, AgentRunStateCancelled, result.State)
	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)

	stored, err := fixture.store.GetAgentRun(
		context.Background(),
		run.SessionID,
		run.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCancelled, stored.State)
}

// A child keeps running after the turn that launched it ends. When it
// finishes, its agent.finished event wakes the idle root, and the woken turn
// reads the answer.
func TestLaunchAgentChildOutlivesItsTurnAndWakesTheRoot(t *testing.T) {
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(
			elelemtest.ToolCall(
				launchAgentCallID,
				toolNameLaunchAgent,
				launchAgentArguments(t, launchAgentInput{
					Task:  "say hi later",
					Agent: launchAgentChildName,
				}),
			),
			elelemtest.Text("launched"),
			elelemtest.Text("woken and read the answer"),
		),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(
			elelemtest.Text("late hi"),
		),
	}).after(runtimeTestAgentName, rootConversation, 2)

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch and leave",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "launched", result.Text)

	run := onlyRun(t, fixture, func() uuid.UUID { return result.SessionID })

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child never finished")
	}

	assert.Equal(t, AgentRunStateCompleted, run.Snapshot().State)

	// Turns list newest first. The woken turn is the second one.
	require.Eventually(t, func() bool {
		turns, err := fixture.store.ListTurns(
			context.Background(),
			result.SessionID,
			session.ListTurnsOptions{},
		)
		if err != nil || len(turns.Items) != 2 {
			return false
		}

		return turns.Items[0].State == models.TurnStateCompleted
	}, launchAgentShutdownBound, launchAgentPollInterval)

	rootRequests := driver.requests(rootConversation)
	require.Len(t, rootRequests, 3)

	wokenConversation := conversationText(rootRequests[2:])
	assert.Contains(t, wokenConversation, agentWakeInstructions)
	assert.Contains(
		t,
		wokenConversation,
		`"response":"late hi"`,
		"the woken turn reads the child's answer from the event",
	)
}

// Cancelling a turn stops the children it started, and the cancelled child's
// report waits rather than waking the root again.
func TestCancelledTurnCancelsTheChildrenItStarted(t *testing.T) {
	driver := heldChildDriver(
		t,
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "hold on",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			launchAgentWaitCallID,
			toolNameWaitMessages,
			`{}`,
		),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	turnDone := make(chan error, 1)

	go func() {
		_, err := fixture.runtime.Run(context.Background(), TurnRequest{
			Message:   "launch and wait",
			Workspace: fixture.workspace,
		})
		turnDone <- err
	}()

	run := onlyRun(t, fixture, fixture.runtime.SessionID)

	cancelled, err := fixture.store.Cancel(context.Background(), run.SessionID)
	require.NoError(t, err)
	assert.True(t, cancelled)

	select {
	case err := <-turnDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the cancelled turn never returned")
	}

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child outlived its cancelled turn")
	}

	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)

	// Finish closes Done before it publishes, so the report may trail it.
	require.Eventually(t, func() bool {
		return fixture.eventBus.Pending(run.SessionID) == 1
	}, launchAgentShutdownBound, launchAgentPollInterval)

	pending := fixture.eventBus.Peek(run.SessionID)
	assert.Equal(t, agentFailedEventType, pending.Notices[0].Type)
	assert.Equal(t, events.DeliveryQueue, pending.Notices[0].Delivery)
}

// Cancelling the session stops children that outlived their turn, and says a
// cancellation was requested even though no turn is running.
func TestCancelSessionStopsChildrenOfEndedTurns(t *testing.T) {
	driver := heldChildDriver(
		t,
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "hold on",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("launched"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch and leave",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	run := onlyRun(t, fixture, func() uuid.UUID { return result.SessionID })
	assert.Equal(t, AgentRunStateRunning, run.Snapshot().State)

	response, err := fixture.runtime.CancelSession(
		context.Background(),
		result.SessionID,
	)
	require.NoError(t, err)
	assert.True(t, response.CancelRequested)

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child was never cancelled")
	}

	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
}

func startDurableAgentRun(
	parentCtx context.Context,
	t *testing.T,
	fixture runtimeFixture,
) (*AgentRunRegistry, *AgentRun, context.Context, *agentRunSink) {
	t.Helper()

	sessionID := uuid.New()
	_, err := fixture.store.CreateOrResume(
		context.Background(),
		&sessionID,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		},
	)
	require.NoError(t, err)

	lease, err := fixture.store.AcquireTurn(
		context.Background(),
		sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleUser,
				Content: "start child",
			}},
		},
	)
	require.NoError(t, err)

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	run, runCtx, err := registry.Start(parentCtx, StartAgentRunInput{
		ID:           uuid.New(),
		ParentTurnID: lease.TurnID,
		RequestID:    uuid.New(),
		Name:         "child",
		Definition:   AgentRunDefinitionStored,
		Depth:        1,
	}, func(run *AgentRun) error {
		_, createErr := fixture.store.CreateAgentRun(
			context.Background(),
			sessionID,
			session.StartAgentRunInput{
				ID:               run.ID,
				ParentTurnID:     run.ParentTurnID,
				ParentToolCallID: run.ParentToolCallID,
				RequestID:        run.RequestID,
				Name:             run.Name,
				Definition:       models.AgentRunDefinitionStored,
				Depth:            int64(run.Depth),
				Workspace:        fixture.workspace,
				ModelReference:   runtimeTestModelReference,
				ModelID:          runtimeTestModelID,
				Task:             "child task",
				Instructions:     "child instructions",
				AllowedToolsJSON: "[]",
				SystemPrompt:     "child system prompt",
				StartedAt:        run.StartedAt,
			},
		)

		return createErr
	})
	require.NoError(t, err)

	return registry, run, runCtx, newAgentRunSink(
		fixture.store,
		sessionID,
		run,
		nil,
		runtimeTestModelReference,
	)
}
