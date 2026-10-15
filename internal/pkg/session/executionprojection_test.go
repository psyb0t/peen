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

func rootProjectionFixture(t *testing.T) (
	executionFixture, *ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, holder.OwnerGenerationID,
		activityTestInput(models.ActivityKindTool))
	require.NoError(t, err)

	return fixture, claim, CompleteActivityInput{
		Output:  json.RawMessage(`{"saved":true}`),
		Context: json.RawMessage(`{}`),
		RootProjection: &ExecutionRootProjection{
			Holder: ExecutionFinishHolder{
				LeaseID:         lease.ID,
				ControlRevision: holder.ControlRevision,
			},
			Messages: []MessageInput{{
				Role:    models.MessageRoleTool,
				Content: "saved tool output", ToolCallID: "readCall",
			}},
			Events: []EventInput{{
				ID:          uuid.New(),
				EventType:   "tool.result",
				PayloadJSON: `{"callId":"readCall"}`,
			}},
		},
	}
}

func TestExecutionProjectionConcurrentCompletionDoesNotDuplicateTranscript(
	t *testing.T,
) {
	t.Parallel()
	fixture, claim, input := rootProjectionFixture(t)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			assert.NoError(t, err)
		})
	}
	group.Wait()
	page, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	events, err := fixture.store.ListEvents(t.Context(), fixture.sessionID,
		ListEventsOptions{EventTypes: []string{"tool.result"}})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	assert.Equal(t, input.RootProjection.Events[0].ID, events.Items[0].ID)
}

func TestExecutionProjectionRollsBackWhenActivityEventCannotCommit(
	t *testing.T,
) {
	t.Parallel()
	fixture, claim, input := rootProjectionFixture(t)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(fixture.root.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateRunning, activity.State)
	assert.Nil(t, activity.OutputJSON)
	assert.Equal(t, emptyActivityJSON, activity.ResolutionJSON)
	page, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	events, err := fixture.store.ListEvents(t.Context(), fixture.sessionID,
		ListEventsOptions{EventTypes: []string{"tool.result"}})
	require.NoError(t, err)
	assert.Empty(t, events.Items)
}

func TestExecutionProjectionRejectsReplacedHolderAndChildActivity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "replaced holder"},
		{name: "child activity", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, claim, input := rootProjectionFixture(t)
			if tc.child {
				child := executionChildForTest(t, fixture, fixture.root)
				var err error
				claim, err = fixture.store.BeginExecutionActivity(t.Context(),
					fixture.sessionID, child.ID, claim.Lease.GenerationID,
					activityTestInput(models.ActivityKindTool))
				require.NoError(t, err)
			} else {
				_, err := fixture.store.RestoreExecutionTurn(t.Context(),
					fixture.sessionID, fixture.root.ID, RestoreExecutionTurnInput{
						OwnerGenerationID: uuid.New(),
						PreviousLeaseID:   input.RootProjection.Holder.LeaseID,
					})
				require.NoError(t, err)
			}
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			activity, err := fixture.store.GetExecutionActivity(t.Context(),
				fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			assert.Nil(t, activity.OutputJSON)
			page, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
				ListMessagesOptions{})
			require.NoError(t, err)
			assert.Len(t, page.Items, 1)
		})
	}
}

func TestExecutionProjectionCommitsWithActivityAndReusesAfterReopen(
	t *testing.T,
) {
	t.Parallel()
	fixture, claim, input := rootProjectionFixture(t)
	receipt, err := fixture.store.CompleteExecutionActivityReceipt(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	require.Len(t, receipt.Events, 1)
	assert.Equal(t, models.ActivityStateCompleted, receipt.Activity.State)
	before, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, before.Items, 2)
	assert.Equal(t, "saved tool output", before.Items[1].Content)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	receipt, err = store.CompleteExecutionActivityReceipt(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Empty(t, receipt.Events)
	after, err := store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	input.RootProjection.Messages[0].Content = "changed"
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionProjectionRefusesInvalidWritesWithoutPartialCompletion(
	t *testing.T,
) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*CompleteActivityInput)
		pause   bool
		wantErr error
	}{
		{
			name: "missing lease", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Holder.LeaseID = uuid.Nil
			},
		},
		{
			name: "wrong lease", wantErr: commerr.ErrConflict,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Holder.LeaseID = uuid.New()
			},
		},
		{
			name: "wrong revision", wantErr: commerr.ErrConflict,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Holder.ControlRevision++
			},
		},
		{
			name: "invalid message", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Messages[0].Role = "invalid"
			},
		},
		{
			name: "invalid event", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Events[0].PayloadJSON = "invalid"
			},
		},
		{
			name: "missing event identity", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Events[0].ID = uuid.Nil
			},
		},
		{name: "paused", pause: true, wantErr: commerr.ErrConflict},
		{
			name: "oversized", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.RootProjection.Messages[0].Content = strings.Repeat("x",
					defaultMaxStoredMessageBytes)
			},
		},
		{
			name: "compaction", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.Compaction = &CompactionInput{}
			},
		},
		{
			name: "finalization", wantErr: commerr.ErrValidationFailed,
			change: func(input *CompleteActivityInput) {
				input.Finish = finishActivityTestResult().Finish
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, claim, input := rootProjectionFixture(t)
			if tc.change != nil {
				tc.change(&input)
			}
			if tc.pause {
				_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
					fixture.root.ID, "retain work")
				require.NoError(t, err)
			}
			before, err := fixture.store.GetExecutionActivity(t.Context(),
				fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			messages, err := fixture.store.ListMessages(t.Context(),
				fixture.sessionID, ListMessagesOptions{})
			require.NoError(t, err)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, tc.wantErr)
			after, err := fixture.store.GetExecutionActivity(t.Context(),
				fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			retained, err := fixture.store.ListMessages(t.Context(),
				fixture.sessionID, ListMessagesOptions{})
			require.NoError(t, err)
			assert.Equal(t, messages, retained)
		})
	}
}
