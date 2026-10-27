package session

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	activityTestStepID      = "exampleStep"
	activityTestName        = "exampleActivity"
	activityTestInputJSON   = `{"arguments":{"path":"example.txt"}}`
	activityTestOutputJSON  = `{"content":"saved output"}`
	activityTestContextJSON = `{"directory":"example","observations":[]}`
	activityTestEvidence    = "inspected the current workspace and recorded output"
)

func activityTestInput(kind models.ActivityKind) ScheduleActivityInput {
	policy := models.ActivityReplayPolicyRetry
	if kind == models.ActivityKindTool {
		policy = models.ActivityReplayPolicyReconcile
	}

	return ScheduleActivityInput{
		StepID: activityTestStepID, Name: activityTestName, Kind: kind,
		ReplayPolicy: policy, Input: json.RawMessage(activityTestInputJSON),
	}
}

func beginActivityForTest(
	t *testing.T,
	fixture executionFixture,
	generationID uuid.UUID,
	kind models.ActivityKind,
) *ActivityClaim {
	t.Helper()
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, generationID, activityTestInput(kind))
	require.NoError(t, err)

	return claim
}

func activityTestResult() CompleteActivityInput {
	return CompleteActivityInput{
		Output:  json.RawMessage(activityTestOutputJSON),
		Context: json.RawMessage(activityTestContextJSON),
	}
}

func TestActivityResultReuseSurvivesDatabaseReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionExecute, claim.Decision)
	assert.EqualValues(t, 1, claim.Lease.Attempt)
	assert.Equal(t, activityTestInputJSON, claim.Activity.InputJSON)
	completed, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, activityTestResult())
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, completed.State)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, activityTestResult())
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	fixture.store = newTestStore(t, handle)
	reused := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, claim.Activity.ID, reused.Activity.ID)
	require.NotNil(t, reused.Activity.OutputJSON)
	assert.Equal(t, activityTestOutputJSON, *reused.Activity.OutputJSON)
	assert.Equal(t, activityTestContextJSON, reused.Activity.ContextJSON)
	history, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, fixture.root.ID, ListEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, history.Items, 4)
}

func TestActivityClaimsFenceReplacedModelAttempts(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	first := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	second := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	assert.Equal(t, first.Activity.ID, second.Activity.ID)
	assert.EqualValues(t, 2, second.Lease.Attempt)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, first.Lease, activityTestResult())
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	stale := second.Lease
	stale.Attempt = 1
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, stale, activityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, second.Lease, activityTestResult())
	require.NoError(t, err)
}

func TestActivityConcurrentClaimHasOneWinner(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	generationID := uuid.New()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			defer func() {
				if recover() != nil {
					results <- ctxerrors.New("activity claim fixture panicked")
				}
			}()
			<-start
			_, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, generationID,
				activityTestInput(models.ActivityKindModel))
			results <- err
		})
	}
	close(start)
	wait.Wait()
	first, second := <-results, <-results
	if first == nil {
		require.ErrorIs(t, second, commerr.ErrConflict)

		return
	}
	require.ErrorIs(t, first, commerr.ErrConflict)
	require.NoError(t, second)
}

func TestActivityInputCannotChangeAfterScheduling(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	input := activityTestInput(models.ActivityKindModel)
	input.Input = json.RawMessage(`{"different":true}`)
	_, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestActivityInputValidation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*ScheduleActivityInput)
	}{
		{name: "missing step", change: func(input *ScheduleActivityInput) {
			input.StepID = ""
		}},
		{name: "long step", change: func(input *ScheduleActivityInput) {
			input.StepID = strings.Repeat("a", maximumActivityIdentityBytes+1)
		}},
		{name: "missing name", change: func(input *ScheduleActivityInput) {
			input.Name = ""
		}},
		{name: "null input", change: func(input *ScheduleActivityInput) {
			input.Input = json.RawMessage(`null`)
		}},
		{name: "array input", change: func(input *ScheduleActivityInput) {
			input.Input = json.RawMessage(`[]`)
		}},
		{name: "malformed input", change: func(input *ScheduleActivityInput) {
			input.Input = json.RawMessage(`{`)
		}},
		{name: "unknown kind", change: func(input *ScheduleActivityInput) {
			input.Kind = models.ActivityKind("unknown")
		}},
		{name: "unknown policy", change: func(input *ScheduleActivityInput) {
			input.ReplayPolicy = models.ActivityReplayPolicy("unknown")
		}},
		{name: "model reconcile", change: func(input *ScheduleActivityInput) {
			input.ReplayPolicy = models.ActivityReplayPolicyReconcile
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			input := activityTestInput(models.ActivityKindModel)
			tc.change(&input)
			_, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, uuid.New(), input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			page, err := fixture.store.ListExecutionActivities(t.Context(),
				fixture.sessionID, fixture.root.ID, ListEventsOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

// A turn context grows with the context window, far past one message, and
// every activity payload carries it.
func TestActivityPayloadsAreBoundedByTheExecutionStateLimit(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		bytes   int
		wantErr error
	}{
		{name: "past the message limit", bytes: 2 * defaultMaxStoredMessageBytes},
		{
			name:    "past the execution state limit",
			bytes:   defaultMaxExecutionStateBytes,
			wantErr: commerr.ErrValidationFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			payload := json.RawMessage(`{"messages":"` +
				strings.Repeat("x", tc.bytes) + `"}`)
			input := activityTestInput(models.ActivityKindTool)
			input.Input = payload
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, uuid.New(), input)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				return
			}
			completed, err := fixture.store.CompleteExecutionActivity(
				t.Context(), fixture.sessionID, claim.Lease,
				CompleteActivityInput{Output: payload, Context: payload})
			require.NoError(t, err)
			assert.Equal(t, string(payload), completed.ContextJSON)
		})
	}
}

func TestPreparationActivityRequiresRetryPolicy(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	input := activityTestInput(models.ActivityKindPreparation)
	input.ReplayPolicy = models.ActivityReplayPolicyReconcile
	_, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	input.ReplayPolicy = models.ActivityReplayPolicyRetry
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityKindPreparation, claim.Activity.Kind)
	assert.Equal(t, ActivityDecisionExecute, claim.Decision)
}
