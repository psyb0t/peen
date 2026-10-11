package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxSession opens a session with one running turn, which every child run
// in these tests belongs to.
func inboxSession(t *testing.T, fixture runtimeFixture) (uuid.UUID, uuid.UUID) {
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
				Content: "start children",
			}},
		},
	)
	require.NoError(t, err)

	return sessionID, lease.TurnID
}

// startInboxRun registers and records a running child with an inbox, the way
// launchAgent does, without running a model.
func startInboxRun(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	name string,
	parent *uuid.UUID,
) (*AgentRun, context.Context) {
	t.Helper()

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	run, runCtx, err := registry.Start(
		context.Background(),
		StartAgentRunInput{
			ParentTurnID:     turnID,
			ParentAgentRunID: parent,
			RequestID:        uuid.New(),
			Name:             name,
			Definition:       AgentRunDefinitionStored,
			Depth:            1,
		},
		func(run *AgentRun) error {
			sink := newAgentRunSink(
				fixture.store,
				sessionID,
				run,
				nil,
				runtimeTestModelReference,
			)

			inbox, err := newAgentInbox(sink, defaultMaxQueuedUserMessages)
			if err != nil {
				return err
			}

			run.inbox = inbox

			_, err = fixture.store.CreateAgentRun(
				context.Background(),
				sessionID,
				session.StartAgentRunInput{
					ID:               run.ID,
					ParentTurnID:     turnID,
					ParentAgentRunID: parent,
					RequestID:        run.RequestID,
					Name:             name,
					Definition:       models.AgentRunDefinitionStored,
					Depth:            1,
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

			return err
		},
	)
	require.NoError(t, err)

	// Nothing runs a model for these runs, so a run the test leaves running
	// is ended here, before the fixture waits for every child to end.
	t.Cleanup(func() {
		if run.Snapshot().State != AgentRunStateRunning {
			return
		}

		registry.Finish(
			context.Background(),
			run,
			AgentRunResult{State: AgentRunStateCancelled},
		)
	})

	return run, contextWithParentAgentRun(runCtx, run)
}

// agentRunEventTypes reads a run's durable event types in order.
func agentRunEventTypes(
	t *testing.T,
	fixture runtimeFixture,
	run *AgentRun,
) []string {
	t.Helper()

	page, err := fixture.store.ListAgentRunEvents(
		context.Background(),
		run.SessionID,
		run.ID,
		session.ListAgentRunEventsOptions{},
	)
	require.NoError(t, err)

	types := make([]string, 0, len(page.Items))
	for _, event := range page.Items {
		types = append(types, event.EventType)
	}

	return types
}

func inboxTestFixture(t *testing.T) runtimeFixture {
	t.Helper()

	return newLaunchAgentFixture(
		t,
		elelemtest.NewScriptedDriver(),
		launchAgentFixtureOptions{},
	)
}

// A delivered message becomes part of the child's durable transcript when a
// round takes it, and only then.
func TestAgentInboxRecordsAMessageWhenARoundTakesIt(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	require.NoError(t, run.inbox.deliver(ctx, "first"))
	require.NoError(t, run.inbox.deliver(ctx, "second"))

	unread, _ := run.inbox.arrival()
	assert.Equal(t, 2, unread)

	// A round that has not taken the messages yet records nothing.
	require.NoError(t, run.inbox.checkpointDelivered(ctx, []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text("task")},
	}))

	transcript := func() []string {
		page, err := fixture.store.ListAgentRunMessages(
			context.Background(),
			sessionID,
			run.ID,
			session.ListAgentRunMessagesOptions{
				Limit: session.MaximumPageLimit,
			},
		)
		require.NoError(t, err)

		contents := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			contents = append(contents, item.Content)
		}

		return contents
	}

	assert.Empty(t, transcript())

	require.NoError(t, run.inbox.checkpointDelivered(ctx, []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text("task")},
		{Role: elelem.RoleUser, Content: elelem.Text("first")},
		{Role: elelem.RoleUser, Content: elelem.Text("second")},
	}))
	assert.Equal(t, []string{"first", "second"}, transcript())

	assert.Equal(
		t,
		[]string{
			EventTypeAgentRunMessageQueued,
			EventTypeAgentRunMessageQueued,
			EventTypeAgentRunMessageDelivered,
			EventTypeAgentRunMessageDelivered,
		},
		agentRunEventTypes(t, fixture, run),
	)
}

// Closing the inbox refuses later messages and hands back the ones no round
// took, so the caller can record them as undelivered.
func TestAgentInboxCloseReturnsUnreadMessagesAndRefusesLaterOnes(
	t *testing.T,
) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	require.NoError(t, run.inbox.deliver(ctx, "never read"))

	assert.Equal(t, []string{"never read"}, run.inbox.close())

	err := run.inbox.deliver(ctx, "too late")
	require.ErrorIs(t, err, session.ErrAgentRunFinished)
}

func TestAgentInboxRefusesAMessageWhenFull(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	for range defaultMaxQueuedUserMessages {
		require.NoError(t, run.inbox.deliver(ctx, "fill"))
	}

	err := run.inbox.deliver(ctx, "one more")
	require.ErrorIs(t, err, commerr.ErrConflict)
	require.ErrorIs(t, err, elelem.ErrUserMessageQueueFull)
}

// The arrival channel closes when a message is accepted, which is what a
// waiting child blocks on.
func TestAgentInboxArrivalSignalsANewMessage(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	unread, arrived := run.inbox.arrival()
	require.Zero(t, unread)

	require.NoError(t, run.inbox.deliver(ctx, "wake up"))

	select {
	case <-arrived:
	default:
		require.FailNow(t, "accepting a message must close the arrival channel")
	}

	payload := agentInboxMessagePayload{}
	page, err := fixture.store.ListAgentRunEvents(
		context.Background(),
		sessionID,
		run.ID,
		session.ListAgentRunEventsOptions{},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NoError(t, json.Unmarshal([]byte(page.Items[0].PayloadJSON), &payload))
	assert.Equal(t, "wake up", payload.Message)
}
