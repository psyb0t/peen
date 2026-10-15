package session

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childProjectionFixture(t *testing.T) (
	executionFixture, *models.Execution, *ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(),
		activityTestInput(models.ActivityKindTool))
	require.NoError(t, err)
	input := activityTestResult()
	input.ChildProjection = &ExecutionChildProjection{
		Messages: []AgentRunMessageInput{{
			Role:    models.MessageRoleTool,
			Content: "saved child result", ToolCallID: "readCall",
			Directory: "/example/changed", SystemPrompt: "retained target prompt",
		}},
		Events: []AgentRunEventInput{{
			ID: uuid.New(), EventType: "agent.run.toolResult",
			PayloadJSON: `{"callId":"readCall"}`,
		}},
	}

	return fixture, child, claim, input
}

func TestExecutionChildProjectionCommitsAndReusesAfterReopening(t *testing.T) {
	t.Parallel()
	fixture, child, claim, input := childProjectionFixture(t)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	before, err := fixture.store.ListAgentRunMessages(t.Context(),
		fixture.sessionID, *child.AgentRunID, ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	assert.Equal(t, "saved child result", before.Items[0].Content)
	events, err := fixture.store.ListAgentRunEvents(t.Context(),
		fixture.sessionID, *child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	assert.Equal(t, input.ChildProjection.Events[0].ID, events.Items[0].ID)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, run.EventCount)
	assert.Equal(t, "/example/changed", run.Workspace)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	after, err := store.ListAgentRunMessages(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	retainedEvents, err := store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, events, retainedEvents)
	input.ChildProjection.Events[0].PayloadJSON = `{"changed":true}`
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionChildProjectionRejectsInvalidPayloadAndRollsBack(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*CompleteActivityInput)
	}{
		{name: "negative revision", change: func(input *CompleteActivityInput) {
			input.ChildProjection.ControlRevision = -1
		}},
		{name: "role", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Messages[0].Role = "invalid"
		}},
		{name: "tool JSON", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Messages[0].ToolCallsJSON = "invalid"
		}},
		{name: "event JSON", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Events[0].PayloadJSON = "invalid"
		}},
		{name: "event type", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Events[0].EventType = ""
		}},
		{name: "event identity", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Events[0].ID = uuid.Nil
		}},
		{name: "size", change: func(input *CompleteActivityInput) {
			input.ChildProjection.Messages[0].Content = strings.Repeat("x",
				defaultMaxStoredMessageBytes)
		}},
		{name: "root payload", change: func(input *CompleteActivityInput) {
			input.RootProjection = &ExecutionRootProjection{}
		}},
		{name: "finish payload", change: func(input *CompleteActivityInput) {
			input.Finish = finishActivityTestResult().Finish
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, child, claim, input := childProjectionFixture(t)
			tc.change(&input)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assertChildProjectionUncommitted(t, fixture, child, claim)
		})
	}
}

func assertChildProjectionUncommitted(
	t *testing.T,
	fixture executionFixture,
	child *models.Execution,
	claim *ActivityClaim,
) {
	t.Helper()
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
	assert.Equal(t, emptyActivityJSON, activity.ResolutionJSON)
	page, err := fixture.store.ListAgentRunMessages(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	events, err := fixture.store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Empty(t, events.Items)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, testWorkspace, run.Workspace)
}

func TestExecutionChildProjectionFencesOwnerAndCounterExhaustion(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		owner    bool
		pause    bool
		cancel   bool
		overflow bool
		wantErr  error
	}{
		{name: "changed owner", owner: true, wantErr: commerr.ErrConflict},
		{name: "paused", pause: true, wantErr: commerr.ErrConflict},
		{name: "cancelled", cancel: true, wantErr: commerr.ErrConflict},
		{name: "exhausted counter", overflow: true, wantErr: commerr.ErrInvalidState},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, child, claim, input := childProjectionFixture(t)
			repository := fixture.store.query.AgentRun
			query := repository.WithContext(t.Context()).Where(
				repository.ID.Eq(*child.AgentRunID))
			if tc.owner {
				_, err := query.UpdateSimple(
					repository.WorkerGenerationID.Value(uuid.NewString()))
				require.NoError(t, err)
			}
			if tc.cancel {
				_, err := query.UpdateSimple(repository.CancelRequested.Value(true))
				require.NoError(t, err)
			}
			if tc.overflow {
				_, err := query.UpdateSimple(repository.EventCount.Value(math.MaxInt64))
				require.NoError(t, err)
			}
			if tc.pause {
				_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
					child.ID, "retain child work")
				require.NoError(t, err)
			}
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, tc.wantErr)
			assertChildProjectionUncommitted(t, fixture, child, claim)
		})
	}
}

func TestExecutionChildProjectionConcurrentRepeatDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	fixture, child, claim, input := childProjectionFixture(t)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			assert.NoError(t, err)
		})
	}
	group.Wait()
	page, err := fixture.store.ListAgentRunMessages(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	events, err := fixture.store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, events.Items, 1)
}

func TestExecutionChildProjectionAcceptsLegacyAndCamelCaseInput(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{"messages":[{"Role":"tool","Content":"saved","ToolCallID":"call"}]}`,
		`{"messages":[{"role":"tool","content":"saved","toolCallId":"call"}]}`,
	} {
		var projection ExecutionChildProjection
		require.NoError(t, json.Unmarshal([]byte(data), &projection))
		require.Len(t, projection.Messages, 1)
		assert.Equal(t, models.MessageRoleTool, projection.Messages[0].Role)
		assert.Equal(t, "saved", projection.Messages[0].Content)
		assert.Equal(t, "call", projection.Messages[0].ToolCallID)
	}
}
