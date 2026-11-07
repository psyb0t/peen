package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	shortWait, err := time.ParseDuration(messageTestShortWait)
	require.NoError(t, err)
	fixture.runtime.askTimeout = shortWait

	output, err := messageDeps(fixture, sessionID).ask(
		childCtx,
		askInput{Question: "keep going?"},
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
