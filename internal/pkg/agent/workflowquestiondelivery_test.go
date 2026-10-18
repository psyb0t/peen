package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedQuestionNoticePublishesOnce(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	sessionID := input.Tool.Round.Execution.SessionID
	assert.Equal(t, 1, fixture.base.eventBus.Pending(sessionID))
	again, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, first.Output, again.Output)
	assert.Equal(t, 1, fixture.base.eventBus.Pending(sessionID))
	batch, err := fixture.base.runtime.drainPendingEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, batch.Notices, 1)
	assert.Equal(t, first.Output.ChildProjection.Transcript.Question.EventID,
		batch.Notices[0].ID)
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Zero(t, fixture.base.eventBus.Pending(sessionID))
	page, err := fixture.base.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStateDelivered, page.Items[0].State)
}

func TestRetainedQuestionNoticeReusesAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	fixture.engine.options.Store = &childQuestionLostAcknowledgement{
		Store: fixture.base.store,
	}
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	sessionID := input.Tool.Round.Execution.SessionID
	assert.Zero(t, fixture.base.eventBus.Pending(sessionID))
	before, err := fixture.base.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture,
		input.Tool.Round)
	replacement.eventBus = events.NewBus(events.Options{})
	fixture.engine.options.Store = store
	dispatch := newWorkflowDispatch(t.Context(), replacement,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	for range 2 {
		reused, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, 1, replacement.eventBus.Pending(sessionID))
	}
	batch := replacement.eventBus.Drain(sessionID)
	require.Len(t, batch.Notices, 1)
	want := before.Items[0]
	assert.Equal(t, want.ID, batch.Notices[0].ID)
	assert.Equal(t, want.CreatedAt, batch.Notices[0].CreatedAt)
	assert.Equal(t, want.Summary, batch.Notices[0].Summary)
	assert.JSONEq(t, want.DataJSON, string(batch.Notices[0].Data))
	after, err := store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestRetainedQuestionNoticeReachesWaitingRoot(t *testing.T) {
	t.Parallel()
	fixture, input, dispatch := childQuestionPhaseFixture(t, "1h")
	parent, err := dispatch.root.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(parent.prepared)
	})
	deps := messageDeps(fixture.base, fixture.input.SessionID)
	deps.userMessages = parent.prepared.userMessages
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	waited := make(chan waitMessagesOutput, 1)
	failed := make(chan error, 1)
	panicked := make(chan any, 1)
	go func() {
		defer func() {
			if value := recover(); value != nil {
				panicked <- value
			}
		}()
		output, err := deps.waitMessages(ctx, waitMessagesInput{})
		if err != nil {
			failed <- err

			return
		}
		waited <- output
	}()
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	select {
	case output := <-waited:
		assert.Equal(t, 1, output.Waiting)
		assert.False(t, output.TimedOut)
	case err := <-failed:
		require.NoError(t, err)
	case value := <-panicked:
		t.Fatalf("root waiter panicked: %v", value)
	case <-time.After(messageTestLongWait):
		t.Fatal("root waiter did not receive retained question")
	}
}

func TestRetainedQuestionNoticeKeepsPendingWhenBusDisabled(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	fixture.base.runtime.eventBus = nil
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	page, err := fixture.base.store.ListSessionNotices(t.Context(),
		input.Tool.Round.Execution.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
}

func TestRetainedQuestionNoticeAdmissionHonorsCancellation(t *testing.T) {
	t.Parallel()
	runtime := &Runtime{}
	release, err := runtime.noticeGate.acquire(t.Context(), true)
	require.NoError(t, err)
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = runtime.publishRetainedQuestion(ctx, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, runtime.publishedQuestionNotices)
}

func TestRetainedQuestionNoticeRetriesAfterChangedBounds(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	fixture.engine.options.Store = &childQuestionLostAcknowledgement{
		Store: fixture.base.store,
	}
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	fixture.base.runtime.eventBus = events.NewBus(events.Options{MaxDataBytes: 1})
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, events.ErrEventTooLarge)
	sessionID := input.Tool.Round.Execution.SessionID
	assert.Zero(t, fixture.base.runtime.eventBus.Pending(sessionID))
	fixture.base.runtime.eventBus = events.NewBus(events.Options{})
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, fixture.base.runtime.eventBus.Pending(sessionID))
	page, err := fixture.base.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
}

func TestValidateRetainedQuestionNotice(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*models.SessionNotice)
	}{
		{name: "different ID", change: func(n *models.SessionNotice) {
			n.ID = uuid.New()
		}},
		{name: "different session", change: func(n *models.SessionNotice) {
			n.SessionID = uuid.New()
		}},
		{name: "different type", change: func(n *models.SessionNotice) {
			n.Type = events.TypeAgentMessage
		}},
		{name: "different source", change: func(n *models.SessionNotice) {
			n.Source = "external"
		}},
		{name: "different run", change: func(n *models.SessionNotice) {
			n.DataJSON = `{"message":"which path?"}`
		}},
		{name: "different question", change: func(n *models.SessionNotice) {
			n.DataJSON = `{}`
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runID := uuid.New()
			child := &models.Execution{SessionID: uuid.New(), AgentRunID: &runID}
			question := &session.ExecutionChildQuestion{
				EventID: uuid.New(), Question: "which path?",
			}
			data, err := json.Marshal(events.AgentNoticeData{
				RunID: runID, Message: question.Question,
			})
			require.NoError(t, err)
			notice := &models.SessionNotice{
				ID: question.EventID, SessionID: child.SessionID,
				Type: events.TypeAgentQuestion, Source: events.AgentSource,
				DataJSON: string(data),
			}
			require.NoError(t, validateRetainedQuestionNotice(notice, child,
				question))
			tc.change(notice)
			require.ErrorIs(t, validateRetainedQuestionNotice(notice, child,
				question), commerr.ErrConflict)
		})
	}
}

func TestRetainedQuestionNoticeRejectsMissingIdentity(t *testing.T) {
	t.Parallel()
	runID := uuid.New()
	testCases := []struct {
		name     string
		child    *models.Execution
		question *session.ExecutionChildQuestion
	}{
		{name: "missing child", question: &session.ExecutionChildQuestion{}},
		{
			name: "missing run", child: &models.Execution{},
			question: &session.ExecutionChildQuestion{},
		},
		{name: "missing question", child: &models.Execution{AgentRunID: &runID}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := &Runtime{}
			_, err := runtime.retainedQuestionNotice(t.Context(), tc.child,
				tc.question)
			require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
		})
	}
}

func TestValidateRetainedQuestionNoticeRejectsMalformedPayload(t *testing.T) {
	t.Parallel()
	runID := uuid.New()
	child := &models.Execution{SessionID: uuid.New(), AgentRunID: &runID}
	question := &session.ExecutionChildQuestion{
		EventID: uuid.New(), Question: "which path?",
	}
	require.ErrorIs(t, validateRetainedQuestionNotice(nil, child, question),
		commerr.ErrNotFound)
	notice := &models.SessionNotice{
		ID: question.EventID, SessionID: child.SessionID,
		Type: events.TypeAgentQuestion, Source: events.AgentSource,
		DataJSON: `{`,
	}
	var syntax *json.SyntaxError
	require.ErrorAs(t, validateRetainedQuestionNotice(notice, child, question),
		&syntax)
}

func endQuestionParent(t *testing.T, fixture executionLoopFixture) {
	t.Helper()
	lease, err := fixture.base.store.RestoreExecutionTurn(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID,
		session.RestoreExecutionTurnInput{OwnerGenerationID: fixture.engine.options.GenerationID})
	require.NoError(t, err)
	query := repositories.Use(fixture.base.handle.GormDB)
	_, err = query.Execution.WithContext(t.Context()).Where(query.Execution.ID.Eq(fixture.input.ExecutionID)).UpdateSimple(
		query.Execution.State.Value(string(models.ExecutionStateCompleted)), query.Execution.CompletedAt.Value(time.Now().UTC()))
	require.NoError(t, err)
	_, err = query.Turn.WithContext(t.Context()).Where(query.Turn.ID.Eq(lease.TurnID)).UpdateSimple(
		query.Turn.State.Value(string(models.TurnStateCompleted)), query.Turn.CompletedAt.Value(time.Now().UTC()))
	require.NoError(t, err)
	fixture.base.store.ReleaseTurn(lease)
	runtime := fixture.base.runtime
	runtime.userMessageQueuesMutex.Lock()
	queue := runtime.userMessageQueues[fixture.input.SessionID]
	delete(runtime.userMessageQueues, fixture.input.SessionID)
	runtime.userMessageQueuesMutex.Unlock()
	if queue != nil {
		queue.close()
		queue.retire()
	}
}

func startQuestionWakeWorker(t *testing.T, fixture executionLoopFixture, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	storage, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	worker, err := NewWorkflowWorker(ctx, runtime, storage, fixture.engine.options.GenerationID, fixture.input.SessionID, nil)
	require.NoError(t, err)
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() { cancel(); require.NoError(t, worker.WaitForCompletion()) })
}

func TestRetainedQuestionWakesIdleNativeRoot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		reopened     bool
		queuedPrefix int
	}{
		{name: "live delivery"},
		{name: "reopened after publication", reopened: true},
		{name: "question beyond first page", reopened: true, queuedPrefix: session.DefaultPageLimit},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, _ := childQuestionPhaseFixture(t, "1h")
			pre, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
			for range tc.queuedPrefix {
				_, err := fixture.base.store.CreateSessionNotice(t.Context(), fixture.input.SessionID, session.CreateSessionNoticeInput{
					Type: wakeTestHandlerType, Source: "test", Summary: "queued event", DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
				})
				require.NoError(t, err)
			}
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("parent read the retained question"))
			registry, err := NewStaticRegistry(map[string]ModelClient{runtimeTestModelReference: {Client: elelem.New(driver), Model: elelem.Model{ID: runtimeTestModelID}}})
			require.NoError(t, err)
			fixture.base.runtime.models = registry
			runtime := fixture.base.runtime
			var question workflowActivityResult[workflowToolPhaseState]
			if tc.reopened {
				question, err = fixture.engine.toolPhase(t.Context(), input)
				require.NoError(t, err)
				assert.Zero(t, driver.Calls())
			}
			endQuestionParent(t, fixture)
			if tc.reopened {
				_, runtime = reopenModelProjectionRuntime(t, fixture, input.Tool.Round)
				runtime.eventBus = events.NewBus(events.Options{})
			}
			startQuestionWakeWorker(t, fixture, runtime)
			if !tc.reopened {
				question, err = fixture.engine.toolPhase(t.Context(), input)
				require.NoError(t, err)
			}
			noticeID := question.Output.ChildProjection.Transcript.Question.EventID
			require.Eventually(t, func() bool {
				page, err := runtime.store.ListSessionNotices(t.Context(), fixture.input.SessionID, session.ListSessionNoticesOptions{ID: &noticeID})
				if err != nil || len(page.Items) != 1 || page.Items[0].State != models.NoticeStateDelivered {
					return false
				}
				pageExecutions, err := runtime.nativeWorkflowRunner().engine.options.Store.SearchExecutions(t.Context(), fixture.input.SessionID,
					session.SearchExecutionsOptions{State: models.ExecutionStateCompleted})

				return err == nil && len(pageExecutions.Items) == 2
			}, workflowTestResultTimeout, 10*time.Millisecond)
			require.Equal(t, 1, driver.Calls())
			requests := driver.Requests()
			require.Len(t, requests, 1)
			assert.Contains(t, strings.Join(messageTexts(requests[0].Messages), "\n"), "which path?")
			require.NoError(t, runtime.nativeWorkflowRunner().restoreWorkflowControls(t.Context()))
			assert.Equal(t, 1, driver.Calls())
		})
	}
}
