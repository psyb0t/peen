package session

import (
	"encoding/json"
	"math"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const childQuestionTestText = "which path should continue?"

func childQuestionFixture(t *testing.T, nested bool) (
	executionFixture, *models.Execution, *models.Execution,
	*ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	parent := fixture.root
	if nested {
		parent = executionChildForTest(t, fixture, parent)
	}

	child := executionChildForTest(t, fixture, parent)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(),
		activityTestInput(models.ActivityKindTool))
	require.NoError(t, err)
	input := activityTestResult()
	input.ChildProjection = &ExecutionChildProjection{
		Events: []AgentRunEventInput{{
			ID: uuid.New(), EventType: events.TypeAgentRunQuestion,
			PayloadJSON: `{"question":"which path should continue?"}`,
		}},
	}
	input.ChildProjection.Question = &ExecutionChildQuestion{
		EventID:  input.ChildProjection.Events[0].ID,
		Question: childQuestionTestText, ParentMessage: "rendered child question",
	}

	return fixture, parent, child, claim, input
}

func TestExecutionChildQuestionRootNoticeSurvivesReopen(t *testing.T) {
	t.Parallel()
	fixture, _, child, claim, input := childQuestionFixture(t, false)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	notices, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, notices.Items, 1)
	notice := notices.Items[0]
	questionEvent := committedChildQuestion(t, fixture, child)
	assert.Equal(t, questionEvent.CreatedAt, notice.CreatedAt)
	assert.Equal(t, input.ChildProjection.Question.EventID, notice.ID)
	assert.Equal(t, events.TypeAgentQuestion, notice.Type)
	assert.Equal(t, models.NoticeDeliveryWake, notice.Delivery)
	assert.Equal(t, models.NoticeStatePending, notice.State)
	var data events.AgentNoticeData
	require.NoError(t, json.Unmarshal([]byte(notice.DataJSON), &data))
	assert.Equal(t, *child.AgentRunID, data.RunID)
	assert.Equal(t, childQuestionTestText, data.Message)
	assert.Equal(t, "exampleChild", data.AgentName)
	assert.Equal(t, events.AgentSource, notice.Source)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	after, err := store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Equal(t, notices, after)
}

func TestExecutionChildQuestionQueuesNestedParentOnce(t *testing.T) {
	t.Parallel()
	fixture, parent, child, claim, input := childQuestionFixture(t, true)
	for range 2 {
		_, err := fixture.store.CompleteExecutionActivity(t.Context(),
			fixture.sessionID, claim.Lease, input)
		require.NoError(t, err)
	}
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	message := page.Items[0]
	questionEvent := committedChildQuestion(t, fixture, child)
	assert.Equal(t, questionEvent.CreatedAt, message.CreatedAt)
	assert.Equal(t, input.ChildProjection.Question.EventID, message.RequestID)
	require.NotNil(t, message.SourceEventID)
	assert.Equal(t, message.RequestID, *message.SourceEventID)
	assert.Equal(t, input.ChildProjection.Question.ParentMessage, message.Message)
	assert.Equal(t, models.InboxMessageStateQueued, message.State)
	notices, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Empty(t, notices.Items)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	after, err := store.ListExecutionMessages(t.Context(), fixture.sessionID,
		parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, page, after)
}

func committedChildQuestion(
	t *testing.T,
	fixture executionFixture,
	child *models.Execution,
) *models.AgentRunEvent {
	t.Helper()
	page, err := fixture.store.ListAgentRunEvents(t.Context(),
		fixture.sessionID, *child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, events.TypeAgentRunQuestion, page.Items[0].EventType)

	return page.Items[0]
}

func TestExecutionChildQuestionRejectsMalformedProjection(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*ExecutionChildProjection)
	}{
		{name: "missing identity", change: func(p *ExecutionChildProjection) {
			p.Question.EventID = uuid.Nil
		}},
		{name: "missing text", change: func(p *ExecutionChildProjection) {
			p.Question.Question = " "
		}},
		{name: "missing parent message", change: func(p *ExecutionChildProjection) {
			p.Question.ParentMessage = " "
		}},
		{name: "missing event", change: func(p *ExecutionChildProjection) {
			p.Question.EventID = uuid.New()
		}},
		{name: "wrong event type", change: func(p *ExecutionChildProjection) {
			p.Events[0].EventType = events.TypeAgentMessage
		}},
		{name: "invalid event payload", change: func(p *ExecutionChildProjection) {
			p.Events[0].PayloadJSON = "invalid"
		}},
		{name: "different question", change: func(p *ExecutionChildProjection) {
			p.Question.Question = "different question"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, parent, child, claim, input := childQuestionFixture(t, true)
			tc.change(input.ChildProjection)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assertChildQuestionUncommitted(t, fixture, parent, child, claim)
		})
	}
}

func assertChildQuestionUncommitted(
	t *testing.T,
	fixture executionFixture,
	parent, child *models.Execution,
	claim *ActivityClaim,
) {
	t.Helper()
	assertChildProjectionUncommitted(t, fixture, child, claim)
	inbox, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, inbox.Items)
	notices, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Empty(t, notices.Items)
}

func TestExecutionChildQuestionRootStateSelectsDelivery(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		state models.ExecutionState
		want  models.NoticeDelivery
	}{
		{state: models.ExecutionStateCompleted, want: models.NoticeDeliveryWake},
		{state: models.ExecutionStateFailed, want: models.NoticeDeliveryWake},
		{state: models.ExecutionStatePaused, want: models.NoticeDeliveryQueue},
	}
	for _, tc := range testCases {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			fixture, parent, _, claim, input := childQuestionFixture(t, false)
			repo := fixture.store.query.Execution
			_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(parent.ID)).
				UpdateSimple(repo.State.Value(string(tc.state)))
			require.NoError(t, err)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			page, err := fixture.store.ListSessionNotices(t.Context(),
				fixture.sessionID, ListSessionNoticesOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, tc.want, page.Items[0].Delivery)
			stored, err := fixture.store.GetExecution(t.Context(),
				fixture.sessionID, parent.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.state, stored.State)
		})
	}
}

func TestExecutionChildQuestionRejectsEndedNestedParent(t *testing.T) {
	t.Parallel()
	for _, state := range []models.ExecutionState{
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			fixture, parent, child, claim, input := childQuestionFixture(t, true)
			repo := fixture.store.query.Execution
			_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(parent.ID)).
				UpdateSimple(repo.State.Value(string(state)))
			require.NoError(t, err)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assertChildQuestionUncommitted(t, fixture, parent, child, claim)
		})
	}
}

func TestExecutionChildQuestionRollsBackWhenParentCounterExhausted(t *testing.T) {
	t.Parallel()
	fixture, parent, child, claim, input := childQuestionFixture(t, true)
	repo := fixture.store.query.Execution
	_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(parent.ID)).
		UpdateSimple(repo.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assertChildQuestionUncommitted(t, fixture, parent, child, claim)
}

func TestExecutionChildQuestionConcurrentRepeatCreatesOneNotice(t *testing.T) {
	t.Parallel()
	fixture, _, _, claim, input := childQuestionFixture(t, false)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			assert.NoError(t, err)
		})
	}
	group.Wait()
	page, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestExecutionChildQuestionRejectsChangedCompletion(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*ExecutionChildProjection)
	}{
		{name: "omitted question", change: func(p *ExecutionChildProjection) {
			p.Question = nil
		}},
		{name: "changed parent message", change: func(p *ExecutionChildProjection) {
			p.Question.ParentMessage = "changed delivery"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, _, claim, input := childQuestionFixture(t, false)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			tc.change(input.ChildProjection)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			page, err := fixture.store.ListSessionNotices(t.Context(),
				fixture.sessionID, ListSessionNoticesOptions{})
			require.NoError(t, err)
			assert.Len(t, page.Items, 1)
		})
	}
}

func TestExecutionChildQuestionFencesParentLineage(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                              string
		foreign, turn, root, run, missing bool
		wantErr                           error
	}{
		{name: "foreign parent", foreign: true, wantErr: commerr.ErrNotFound},
		{name: "wrong turn", turn: true, wantErr: commerr.ErrConflict},
		{name: "wrong root", root: true, wantErr: commerr.ErrConflict},
		{name: "wrong parent run", run: true, wantErr: commerr.ErrConflict},
		{name: "missing parent", missing: true, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, parent, child, claim, _ := childQuestionFixture(t, true)
			other := newTestSession(t.Context(), t, fixture.store)
			turn := startAgentRunTestTurn(t.Context(), t, fixture.store, other.ID)
			foreign, err := fixture.store.CreateExecution(t.Context(), other.ID,
				executionTestInput(turn.TurnID))
			require.NoError(t, err)
			storedRun, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			changedChild, changedRun := *child, *storedRun
			if tc.foreign {
				changedChild.ParentExecutionID = &foreign.ID
			}
			if tc.turn {
				changedChild.RootTurnID = turn.TurnID
			}
			if tc.root {
				changedChild.RootExecutionID = foreign.ID
			}
			if tc.run {
				changedRun.ParentAgentRunID = child.AgentRunID
			}
			if tc.missing {
				changedChild.ParentExecutionID = nil
			}
			_, err = fixture.store.childQuestionParent(t.Context(),
				fixture.store.query, &changedChild, &changedRun)
			require.ErrorIs(t, err, tc.wantErr)
			assertChildQuestionUncommitted(t, fixture, parent, child, claim)
		})
	}
}

func TestExecutionChildQuestionNoticeFailureRollsBackCompletion(t *testing.T) {
	t.Parallel()
	fixture, _, child, claim, input := childQuestionFixture(t, false)
	notice, err := fixture.store.CreateSessionNotice(t.Context(),
		fixture.sessionID, CreateSessionNoticeInput{
			ID:   input.ChildProjection.Question.EventID,
			Type: "app.example", Source: "example", Summary: "unrelated notice",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
	assertChildProjectionUncommitted(t, fixture, child, claim)
	page, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, notice, page.Items[0])
}

func TestExecutionChildQuestionSurvivesFullParentInbox(t *testing.T) {
	t.Parallel()
	fixture, parent, _, claim, input := childQuestionFixture(t, true)
	accepted := inboxTestInput()
	accepted.Capacity = 1
	accepted.Parts = nil
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, parent.ID, accepted)
	require.NoError(t, err)
	accepted.RequestID = uuid.New()
	_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, parent.ID, accepted)
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, accepted.Message, page.Items[0].Message)
	assert.Equal(t, input.ChildProjection.Question.EventID,
		page.Items[1].RequestID)
	assert.Equal(t, input.ChildProjection.Question.ParentMessage,
		page.Items[1].Message)
}

func TestExecutionChildQuestionDoesNotResumePausedParent(t *testing.T) {
	t.Parallel()
	fixture, parent, _, claim, input := childQuestionFixture(t, true)
	repo := fixture.store.query.Execution
	_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(parent.ID)).
		UpdateSimple(repo.State.Value(string(models.ExecutionStatePaused)))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		parent.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.Equal(t, parent.ControlRevision, stored.ControlRevision)
}
