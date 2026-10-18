package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitMessagesFindsNoticeAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture,
		input.Tool.Round)
	replacement.eventBus = events.NewBus(events.Options{})
	dispatch := newWorkflowDispatch(t.Context(), replacement,
		fixture.engine.options.GenerationID, nil)
	parent, err := dispatch.root.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	t.Cleanup(func() {
		replacement.closeActiveUserMessageQueue(parent.prepared)
	})
	deps := &launchAgentDeps{
		runtime: replacement, sessionID: fixture.input.SessionID,
		userMessages: parent.prepared.userMessages,
	}
	output, err := deps.waitMessages(t.Context(),
		waitMessagesInput{Timeout: "1ns"})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Waiting)
	assert.False(t, output.TimedOut)
	assert.Zero(t, replacement.eventBus.Pending(fixture.input.SessionID))
	page, err := store.ListSessionNotices(t.Context(), fixture.input.SessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
}

func TestWaitMessagesCountsDurableNoticesWithoutLiveDuplicates(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		count    int
		live     bool
		disabled bool
	}{
		{name: "empty"},
		{name: "one", count: 1},
		{name: "multiple pages", count: session.DefaultPageLimit + 3},
		{name: "live duplicates", count: 2, live: true},
		{name: "bus disabled", count: 2, disabled: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := inboxTestFixture(t)
			sessionID, _ := inboxSession(t, fixture)
			otherID, _ := inboxSession(t, fixture)
			create := func(id uuid.UUID) *models.SessionNotice {
				t.Helper()
				notice, err := fixture.store.CreateSessionNotice(t.Context(), id,
					session.CreateSessionNoticeInput{
						Type: "app.ready", Source: externalEventSource, Summary: "ready",
						DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
					})
				require.NoError(t, err)

				return notice
			}
			create(sessionID)
			_, err := fixture.store.DrainSessionNotices(t.Context(), sessionID)
			require.NoError(t, err)
			create(otherID)
			for range tc.count {
				notice := create(sessionID)
				if tc.live {
					_, err := fixture.eventBus.Publish(events.Notice{
						ID: notice.ID, SessionID: sessionID, Type: notice.Type,
						Source: notice.Source, Summary: notice.Summary,
					})
					require.NoError(t, err)
				}
			}
			if tc.disabled {
				fixture.runtime.eventBus = nil
			}
			queue, err := elelem.NewUserMessageQueue(defaultMaxQueuedUserMessages)
			require.NoError(t, err)
			require.NoError(t, queue.EnqueueText("queued user input"))
			deps := messageDeps(fixture, sessionID)
			deps.userMessages = &activeUserMessageQueue{
				queue: queue, arrived: make(chan struct{}),
			}
			output, err := deps.waitMessages(t.Context(),
				waitMessagesInput{Timeout: "1ns"})
			require.NoError(t, err)
			assert.Equal(t, tc.count+1, output.Waiting)
			assert.False(t, output.TimedOut)
			count, err := fixture.runtime.pendingSessionNoticeCount(t.Context(),
				sessionID)
			require.NoError(t, err)
			assert.Equal(t, tc.count, count)
		})
	}
}

type noticeReadFailureStore struct {
	*session.Store
}

func (s *noticeReadFailureStore) ListSessionNotices(
	_ context.Context,
	_ uuid.UUID,
	_ session.ListSessionNoticesOptions,
) (*session.NoticePage, error) {
	return nil, ctxerrors.Wrap(commerr.ErrConflict, "injected notice read failure")
}

func TestWaitMessagesReturnsDurableNoticeReadFailure(t *testing.T) {
	t.Parallel()
	fixture := inboxTestFixture(t)
	sessionID, _ := inboxSession(t, fixture)
	fixture.runtime.store = &noticeReadFailureStore{Store: fixture.store}
	queue, err := elelem.NewUserMessageQueue(defaultMaxQueuedUserMessages)
	require.NoError(t, err)
	deps := messageDeps(fixture, sessionID)
	deps.userMessages = &activeUserMessageQueue{
		queue: queue, arrived: make(chan struct{}),
	}
	_, err = deps.waitMessages(t.Context(), waitMessagesInput{Timeout: "1ns"})
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestWaitMessagesHonorsCancellationBeforeNoticeRead(t *testing.T) {
	t.Parallel()
	fixture := inboxTestFixture(t)
	sessionID, _ := inboxSession(t, fixture)
	queue, err := elelem.NewUserMessageQueue(defaultMaxQueuedUserMessages)
	require.NoError(t, err)
	deps := messageDeps(fixture, sessionID)
	deps.userMessages = &activeUserMessageQueue{
		queue: queue, arrived: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = deps.waitMessages(ctx, waitMessagesInput{})
	require.ErrorIs(t, err, context.Canceled)
}

const (
	messageTestShortWait = "50ms"
	messageTestLongWait  = time.Minute
)

func messageDeps(fixture runtimeFixture, sessionID uuid.UUID) *launchAgentDeps {
	return &launchAgentDeps{runtime: fixture.runtime, sessionID: sessionID}
}

// unreadInbox returns the messages a child's inbox holds that no round has
// taken yet.
func unreadInbox(run *AgentRun) []string {
	run.inbox.mutex.Lock()
	defer run.inbox.mutex.Unlock()

	return append([]string(nil), run.inbox.pending...)
}

// The root writes to a running child. The message waits in the child's inbox,
// tagged with the root as its sender.
func TestSendMessageFromTheRootReachesAChildsInbox(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, _ := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	output, err := messageDeps(fixture, sessionID).sendMessage(
		context.Background(),
		sendMessageInput{To: run.ID.String(), Message: "use the fast path"},
	)
	require.NoError(t, err)
	assert.True(t, output.Delivered)
	assert.Equal(t, run.ID.String(), output.To)

	expected, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: runtimeTestAgentName,
		Run:  rootAgentRunReference,
		Text: "use the fast path",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{expected}, unreadInbox(run))
}

// A child's message to another child names the sending run.
func TestSendMessageBetweenChildrenNamesTheSender(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	sender, senderCtx := startInboxRun(
		t,
		fixture,
		sessionID,
		turnID,
		"sender",
		nil,
	)
	recipient, _ := startInboxRun(
		t,
		fixture,
		sessionID,
		turnID,
		"recipient",
		nil,
	)

	_, err := messageDeps(fixture, sessionID).sendMessage(
		senderCtx,
		sendMessageInput{To: recipient.ID.String(), Message: "found it"},
	)
	require.NoError(t, err)

	expected, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: "sender",
		Run:  sender.ID.String(),
		Text: "found it",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{expected}, unreadInbox(recipient))
}

// A child's message to the root is an agent.message session event that asks
// to wake the root.
func TestSendMessageFromAChildToTheRootPublishesAnEvent(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	output, err := messageDeps(fixture, sessionID).sendMessage(
		ctx,
		sendMessageInput{To: rootAgentRunReference, Message: "halfway there"},
	)
	require.NoError(t, err)
	assert.True(t, output.Delivered)

	batch := fixture.eventBus.Drain(sessionID)
	require.Len(t, batch.Notices, 1)

	notice := batch.Notices[0]
	assert.Equal(t, events.TypeAgentMessage, notice.Type)
	assert.Equal(t, agentEventSource, notice.Source)
	assert.Equal(t, events.DeliveryWake, notice.Delivery)

	data := agentNoticeData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, run.ID, data.RunID)
	assert.Equal(t, "child", data.AgentName)
	assert.Equal(t, "halfway there", data.Message)
}

func TestSendMessageRefusesBadTargets(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, runCtx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)
	finished, _ := startInboxRun(t, fixture, sessionID, turnID, "done", nil)

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	// A finished run is gone from a fresh process, which is the case a
	// restarted worker meets: only the durable row says how it ended.
	result := fixture.runtime.finishAgentRun(
		context.Background(),
		registry,
		finished.inbox.sink,
		finished,
		nil,
		&wrappedCancelError{},
	)
	require.Equal(t, AgentRunStateCancelled, result.State)
	registry.mu.Lock()
	delete(registry.runs, finished.ID)
	registry.mu.Unlock()

	testCases := []struct {
		name        string
		isFromChild bool
		input       sendMessageInput
		wantErr     error
	}{
		{
			name:    "empty message",
			input:   sendMessageInput{To: run.ID.String()},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "not an ID",
			input:   sendMessageInput{To: "someone", Message: "hi"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "the root to itself",
			input: sendMessageInput{
				To:      rootAgentRunReference,
				Message: "hi",
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:        "a child to itself",
			isFromChild: true,
			input:       sendMessageInput{To: run.ID.String(), Message: "hi"},
			wantErr:     commerr.ErrValidationFailed,
		},
		{
			name:    "an unknown run",
			input:   sendMessageInput{To: uuid.NewString(), Message: "hi"},
			wantErr: session.ErrAgentRunNotFound,
		},
		{
			name: "a finished run",
			input: sendMessageInput{
				To:      finished.ID.String(),
				Message: "hi",
			},
			wantErr: session.ErrAgentRunFinished,
		},
	}

	senderContexts := map[bool]context.Context{
		false: context.Background(),
		true:  runCtx,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := messageDeps(fixture, sessionID).sendMessage(
				senderContexts[tc.isFromChild],
				tc.input,
			)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// A child's message too large for a session event is refused, so the child
// learns to shorten it instead of the bus dropping its data.
func TestSendMessageToTheRootRefusesAnOversizedMessage(t *testing.T) {
	t.Parallel()

	fixture := newLaunchAgentFixture(
		t,
		elelemtest.NewScriptedDriver(),
		launchAgentFixtureOptions{
			Customize: func(options *RuntimeOptions) {
				options.Events = events.NewBus(events.Options{MaxDataBytes: 64})
			},
		},
	)
	sessionID, turnID := inboxSession(t, fixture)
	_, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	_, err := messageDeps(fixture, sessionID).sendMessage(
		ctx,
		sendMessageInput{
			To:      rootAgentRunReference,
			Message: string(make([]byte, 128)),
		},
	)
	require.ErrorIs(t, err, events.ErrEventTooLarge)
}

func TestWaitMessagesReturnsWhenAChildsMessageArrives(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	waited := make(chan waitMessagesOutput, 1)
	failed := make(chan error, 1)

	go func() {
		output, err := messageDeps(fixture, sessionID).waitMessages(
			ctx,
			waitMessagesInput{},
		)
		if err != nil {
			failed <- err

			return
		}

		waited <- output
	}()

	require.NoError(t, run.inbox.deliver(ctx, "here you go"))

	select {
	case output := <-waited:
		assert.Equal(t, 1, output.Waiting)
		assert.False(t, output.TimedOut)
	case err := <-failed:
		require.NoError(t, err)
	case <-time.After(messageTestLongWait):
		require.FailNow(t, "waitMessages never saw the message")
	}
}

func TestWaitMessagesTimesOutWithNothingWaiting(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	_, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	output, err := messageDeps(fixture, sessionID).waitMessages(
		ctx,
		waitMessagesInput{Timeout: messageTestShortWait},
	)
	require.NoError(t, err)
	assert.True(t, output.TimedOut)
	assert.Zero(t, output.Waiting)

	_, err = messageDeps(fixture, sessionID).waitMessages(
		ctx,
		waitMessagesInput{Timeout: "soon"},
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// The root waits on session events too, which is how a finished child's
// report reaches a root that is waiting for it.
func TestWaitMessagesForTheRootReturnsOnASessionEvent(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, _ := inboxSession(t, fixture)

	queue, err := elelem.NewUserMessageQueue(defaultMaxQueuedUserMessages)
	require.NoError(t, err)

	deps := messageDeps(fixture, sessionID)
	deps.userMessages = &activeUserMessageQueue{
		queue:   queue,
		arrived: make(chan struct{}),
	}

	waited := make(chan waitMessagesOutput, 1)
	failed := make(chan error, 1)

	go func() {
		output, err := deps.waitMessages(
			context.Background(),
			waitMessagesInput{},
		)
		if err != nil {
			failed <- err

			return
		}

		waited <- output
	}()

	// The waiter subscribes before it checks what is pending, so an event
	// published at any point after the call starts is seen.
	_, err = fixture.runtime.PublishEvent(
		context.Background(),
		events.Notice{
			SessionID: sessionID,
			Type:      "app.ready",
			Source:    externalEventSource,
			Summary:   "ready",
		},
	)
	require.NoError(t, err)

	select {
	case output := <-waited:
		assert.Equal(t, 1, output.Waiting)
	case err := <-failed:
		require.NoError(t, err)
	case <-time.After(messageTestLongWait):
		require.FailNow(t, "waitMessages never saw the event")
	}
}

func TestStopAgentCancelsAChildButNotItself(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, runCtx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	_, err := messageDeps(fixture, sessionID).stopAgent(
		runCtx,
		stopAgentInput{AgentRunID: run.ID},
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	require.NoError(t, runCtx.Err())

	output, err := messageDeps(fixture, sessionID).stopAgent(
		context.Background(),
		stopAgentInput{AgentRunID: run.ID},
	)
	require.NoError(t, err)
	assert.Equal(t, run.ID, output.AgentRunID)
	assert.True(t, output.CancelRequested)
	assert.ErrorIs(t, runCtx.Err(), context.Canceled)

	_, err = messageDeps(fixture, sessionID).stopAgent(
		context.Background(),
		stopAgentInput{AgentRunID: uuid.New()},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

// A child of the root asks through an agent.question event and waits on its
// own inbox. The root's answer ends the wait, and the run's events show the
// question opened and closed.
func TestAskFromAChildOfTheRootWaitsForTheAnswer(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, ctx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	asked := make(chan askOutput, 1)
	failed := make(chan error, 1)

	go func() {
		output, err := messageDeps(fixture, sessionID).ask(
			ctx,
			askInput{Question: "which branch?"},
		)
		if err != nil {
			failed <- err

			return
		}

		asked <- output
	}()

	require.Eventually(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 1
	}, messageTestLongWait, launchAgentPollInterval)

	notice := fixture.eventBus.Drain(sessionID).Notices[0]
	assert.Equal(t, events.TypeAgentQuestion, notice.Type)
	assert.Equal(t, events.DeliveryWake, notice.Delivery)

	data := agentNoticeData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, run.ID, data.RunID)
	assert.Equal(t, "which branch?", data.Message)

	_, err := messageDeps(fixture, sessionID).sendMessage(
		context.Background(),
		sendMessageInput{To: run.ID.String(), Message: "main"},
	)
	require.NoError(t, err)

	select {
	case output := <-asked:
		assert.True(t, output.Answered)
		assert.False(t, output.TimedOut)
	case err := <-failed:
		require.NoError(t, err)
	case <-time.After(messageTestLongWait):
		require.FailNow(t, "ask never saw the answer")
	}

	assert.Equal(
		t,
		[]string{
			EventTypeAgentRunQuestion,
			EventTypeAgentRunMessageQueued,
			EventTypeAgentRunAnswer,
		},
		agentRunEventTypes(t, fixture, run),
	)
}

func TestAskCancellationLeavesChildQuestionOpen(t *testing.T) {
	t.Parallel()
	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, childCtx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)
	ctx, cancel := context.WithCancel(childCtx)
	t.Cleanup(cancel)
	finished := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				finished <- ctxerrors.New("question cancellation fixture panic")
			}
		}()
		_, err := messageDeps(fixture, sessionID).ask(ctx,
			askInput{Question: "which branch?"})
		finished <- err
	}()
	require.Eventually(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 1
	}, messageTestLongWait, launchAgentPollInterval)
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(messageTestLongWait):
		t.Fatal("question activity ignored cancellation")
	}
	assert.Equal(t, []string{EventTypeAgentRunQuestion},
		agentRunEventTypes(t, fixture, run))
}

func TestAskPublicationCancellationLeavesChildQuestionOpen(t *testing.T) {
	t.Parallel()
	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, childCtx := startInboxRun(t, fixture, sessionID, turnID, "child", nil)
	ctx, cancel := context.WithCancel(childCtx)
	t.Cleanup(cancel)
	run.inbox.sink.liveSink = func(event Event) error {
		if event.Type == EventTypeAgentRunQuestion {
			cancel()
		}

		return nil
	}
	_, err := messageDeps(fixture, sessionID).ask(ctx,
		askInput{Question: "which branch?"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{EventTypeAgentRunQuestion},
		agentRunEventTypes(t, fixture, run))
	notices, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Empty(t, notices.Items)
	assert.Empty(t, fixture.eventBus.Drain(sessionID).Notices)
}

func TestAskParentDeliveryFailurePreservesInterruption(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		failure   error
		cancel    bool
		wantClose bool
	}{
		{name: "cancelled transport", failure: context.Canceled},
		{name: "expired transport", failure: context.DeadlineExceeded},
		{name: "cancelled caller", failure: commerr.ErrConflict, cancel: true},
		{name: "delivery refused", failure: commerr.ErrConflict, wantClose: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := inboxTestFixture(t)
			sessionID, turnID := inboxSession(t, fixture)
			parent, _ := startInboxRun(t, fixture, sessionID, turnID,
				"parent", nil)
			child, childCtx := startInboxRun(t, fixture, sessionID, turnID,
				"child", &parent.ID)
			ctx, cancel := context.WithCancel(childCtx)
			t.Cleanup(cancel)
			parent.inbox.sink.liveSink = func(Event) error {
				if tc.cancel {
					cancel()
				}

				return ctxerrors.Wrap(tc.failure, "interrupt parent publication")
			}
			_, err := messageDeps(fixture, sessionID).ask(ctx,
				askInput{Question: "which branch?"})
			require.ErrorIs(t, err, tc.failure)
			want := []string{EventTypeAgentRunQuestion}
			if tc.wantClose {
				want = append(want, EventTypeAgentRunAnswer)
			}

			assert.Equal(t, want, agentRunEventTypes(t, fixture, child))
			assert.Equal(t, []string{EventTypeAgentRunMessageQueued},
				agentRunEventTypes(t, fixture, parent))
			assert.Empty(t, unreadInbox(parent))
		})
	}
}

func TestAskCancellationLeavesRootQuestionOpen(t *testing.T) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	shared.prepared.turn.observer = func(event Event) {
		if event.Type == EventTypeQuestionAsked {
			cancel()
		}
	}
	deps := messageDeps(fixture.base, fixture.input.SessionID)
	deps.userMessages = shared.prepared.userMessages
	_, err := deps.askUser(ctx, "which branch?", 0)
	require.ErrorIs(t, err, context.Canceled)
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeQuestionAsked, EventTypeQuestionAnswered},
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, EventTypeQuestionAsked, page.Items[0].EventType)
}

func TestAskRootRecordsAnswerOrTimeout(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		answered bool
	}{
		{name: "answer", answered: true},
		{name: "timeout"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, shared := newRootCallbackFixture(t,
				elelemtest.NewScriptedDriver())
			queue := shared.prepared.userMessages
			if tc.answered {
				require.NoError(t, queue.enqueue(t.Context(), TurnRequest{
					Message: "continue",
				}))
			}
			deps := messageDeps(fixture.base, fixture.input.SessionID)
			deps.userMessages = queue
			output, err := deps.askUser(t.Context(), "which branch?",
				time.Nanosecond)
			require.NoError(t, err)
			assert.Equal(t, tc.answered, output.Answered)
			assert.Equal(t, !tc.answered, output.TimedOut)
			page, err := fixture.base.store.ListEvents(t.Context(),
				fixture.input.SessionID, session.ListEventsOptions{
					EventTypes: []string{EventTypeQuestionAnswered},
				})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			var answer agentAnswerPayload
			require.NoError(t, json.Unmarshal([]byte(page.Items[0].PayloadJSON),
				&answer))
			assert.Equal(t, tc.answered, answer.Answered)
		})
	}
}

// A nested child asks its parent run, whose inbox gets a question message
// naming the asker.
func TestAskFromANestedChildReachesItsParentsInbox(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	parent, _ := startInboxRun(t, fixture, sessionID, turnID, "parent", nil)
	child, childCtx := startInboxRun(
		t,
		fixture,
		sessionID,
		turnID,
		"child",
		&parent.ID,
	)

	output, err := messageDeps(fixture, sessionID).ask(
		childCtx,
		askInput{Question: "keep going?", Timeout: messageTestShortWait},
	)
	require.NoError(t, err)
	assert.False(t, output.Answered)
	assert.True(t, output.TimedOut)

	expected, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: "child",
		Run:  child.ID.String(),
		Kind: messageKindQuestion,
		Text: "keep going?",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{expected}, unreadInbox(parent))
	assert.Empty(
		t,
		fixture.eventBus.Drain(sessionID).Notices,
		"a nested question never reaches the root",
	)

	page, err := fixture.store.ListAgentRunEvents(
		context.Background(),
		sessionID,
		child.ID,
		session.ListAgentRunEventsOptions{},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	answer := agentAnswerPayload{}
	require.NoError(t, json.Unmarshal(
		[]byte(page.Items[1].PayloadJSON),
		&answer,
	))
	assert.False(t, answer.Answered, "a timed out question closes unanswered")
}

// A person can write to a running child. The message starts no turn and lands
// in the child's inbox tagged as coming from the user.
func TestRunMessageToAChildQueuesItFromTheUser(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	run, _ := startInboxRun(t, fixture, sessionID, turnID, "child", nil)

	result, err := fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{
			SessionID: &sessionID,
			Message:   "try the main branch",
			To:        &run.ID,
		},
		uuid.New(),
		nil,
	)
	require.NoError(t, err)
	assert.True(t, result.Queued)
	assert.Equal(t, sessionID, result.SessionID)

	expected, err := renderAgentMessage(agentMessage{
		From: messageSenderUser,
		Text: "try the main branch",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{expected}, unreadInbox(run))

	model := runtimeTestModelReference
	_, err = fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{
			SessionID: &sessionID,
			Message:   "switch models",
			Model:     &model,
			To:        &run.ID,
		},
		uuid.New(),
		nil,
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)

	unknown := uuid.New()
	_, err = fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{
			SessionID: &sessionID,
			Message:   "anyone there?",
			To:        &unknown,
		},
		uuid.New(),
		nil,
	)
	require.ErrorIs(t, err, session.ErrAgentRunNotFound)
}

// A waiting child reads as waiting through the API, with its question, and
// the list can keep only waiting runs or the children of one parent.
func TestListSessionAgentRunsShowsWaitingRunsAndFiltersByParent(t *testing.T) {
	t.Parallel()

	fixture := inboxTestFixture(t)
	sessionID, turnID := inboxSession(t, fixture)
	parent, _ := startInboxRun(t, fixture, sessionID, turnID, "parent", nil)
	child, childCtx := startInboxRun(
		t,
		fixture,
		sessionID,
		turnID,
		"child",
		&parent.ID,
	)

	require.NoError(t, child.inbox.sink.emit(
		childCtx,
		EventTypeAgentRunQuestion,
		agentQuestionPayload{Question: "which file?"},
	))

	waiting := api.ListSessionAgentRunsParamsStateWaiting
	page, err := fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		sessionID,
		api.ListSessionAgentRunsParams{State: &waiting},
	)
	require.NoError(t, err)
	require.Len(t, page.Agents, 1)
	assert.Equal(t, child.ID, page.Agents[0].AgentRunId)
	assert.True(t, page.Agents[0].Waiting)
	require.NotNil(t, page.Agents[0].Question)
	assert.Equal(t, "which file?", *page.Agents[0].Question)

	stored, err := fixture.runtime.GetSessionAgentRun(
		context.Background(),
		sessionID,
		child.ID,
	)
	require.NoError(t, err)
	assert.True(t, stored.Waiting)

	parentFilter := parent.ID.String()
	page, err = fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		sessionID,
		api.ListSessionAgentRunsParams{ParentAgentRunId: &parentFilter},
	)
	require.NoError(t, err)
	require.Len(t, page.Agents, 1)
	assert.Equal(t, child.ID, page.Agents[0].AgentRunId)

	rootFilter := rootAgentRunReference
	page, err = fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		sessionID,
		api.ListSessionAgentRunsParams{ParentAgentRunId: &rootFilter},
	)
	require.NoError(t, err)
	require.Len(t, page.Agents, 1)
	assert.Equal(t, parent.ID, page.Agents[0].AgentRunId)
	assert.False(t, page.Agents[0].Waiting)

	badFilter := "somebody"
	_, err = fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		sessionID,
		api.ListSessionAgentRunsParams{ParentAgentRunId: &badFilter},
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestAskTimeoutForCombinesTheCallAndTheDeployment(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		deployment time.Duration
		requested  string
		want       time.Duration
	}{
		{name: "neither", want: 0},
		{name: "deployment only", deployment: time.Hour, want: time.Hour},
		{name: "call only", requested: "5m", want: 5 * time.Minute},
		{
			name:       "call shortens",
			deployment: time.Hour,
			requested:  "5m",
			want:       5 * time.Minute,
		},
		{
			name:       "call cannot lengthen",
			deployment: time.Minute,
			requested:  "1h",
			want:       time.Minute,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runtime := &Runtime{askTimeout: tc.deployment}

			got, err := runtime.askTimeoutFor(tc.requested)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := (&Runtime{}).askTimeoutFor("-1s")
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
