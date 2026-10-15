package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentResumeFixture(t *testing.T) (executionFixture, *models.Execution, *ActivityClaim) {
	t.Helper()
	fixture := newExecutionFixture(t)
	target := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID, target.ID, executionTestPauseReason)
	require.NoError(t, err)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)

	return fixture, target, claim
}

func TestAgentResumeRetainsSubtreeAndControlProvenance(t *testing.T) {
	t.Parallel()
	fixture, target, claim := agentResumeFixture(t)
	grandchild := executionChildForTest(t, fixture, target)
	sibling := executionChildForTest(t, fixture, fixture.root)
	changed, err := fixture.store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, target.ID, ResumeExecutionInput{SourceLease: claim.Lease, ControlRevision: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{target.ID, grandchild.ID}, changed)
	for _, id := range changed {
		stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, id)
		require.NoError(t, err)
		assert.Equal(t, models.ExecutionStateQueued, stored.State)
		assert.Equal(t, executionTestContext, stored.ContextJSON)
		events, err := fixture.store.ListExecutionEvents(t.Context(), fixture.sessionID, id, ListEventsOptions{})
		require.NoError(t, err)
		last := events.Items[len(events.Items)-1]
		assert.Equal(t, string(ExecutionEventTypeResumed), last.EventType)
		var provenance struct {
			SourceExecutionID uuid.UUID `json:"sourceExecutionId"`
			ResumeExecutionInput
		}
		require.NoError(t, json.Unmarshal([]byte(last.PayloadJSON), &provenance))
		assert.Equal(t, fixture.root.ID, provenance.SourceExecutionID)
		assert.Equal(t, claim.Lease, provenance.SourceLease)
		assert.EqualValues(t, 1, provenance.ControlRevision)
	}
	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, sibling.ID)
	require.NoError(t, err)
	assert.Zero(t, stored.ControlRevision)
	_, err = fixture.store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, target.ID, ResumeExecutionInput{SourceLease: claim.Lease, ControlRevision: 1})
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestAgentResumeRefusesStaleCallerOrTarget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*testing.T, executionFixture, *models.Execution, *ActivityClaim, *ResumeExecutionInput) uuid.UUID
		wantErr error
	}{
		{name: "source paused by Stop", wantErr: commerr.ErrConflict, change: func(t *testing.T, f executionFixture, _ *models.Execution, _ *ActivityClaim, _ *ResumeExecutionInput) uuid.UUID {
			t.Helper()
			_, err := f.store.Cancel(t.Context(), f.sessionID)
			require.NoError(t, err)

			return uuid.Nil
		}},
		{name: "completed source activity", wantErr: commerr.ErrConflict, change: func(t *testing.T, f executionFixture, _ *models.Execution, claim *ActivityClaim, _ *ResumeExecutionInput) uuid.UUID {
			t.Helper()
			_, err := f.store.CompleteExecutionActivity(t.Context(), f.sessionID, claim.Lease, CompleteActivityInput{Output: json.RawMessage(`{}`), Context: json.RawMessage(executionTestContext)})
			require.NoError(t, err)

			return uuid.Nil
		}},
		{name: "model cannot issue control", wantErr: commerr.ErrConflict, change: func(t *testing.T, f executionFixture, _ *models.Execution, claim *ActivityClaim, _ *ResumeExecutionInput) uuid.UUID {
			t.Helper()
			repository := f.store.query.ExecutionActivity
			_, err := repository.WithContext(t.Context()).Where(repository.ID.Eq(claim.Activity.ID)).UpdateSimple(
				repository.Kind.Value(string(models.ActivityKindModel)),
				repository.ReplayPolicy.Value(string(models.ActivityReplayPolicyRetry)),
			)
			require.NoError(t, err)

			return uuid.Nil
		}},
		{name: "changed target revision", wantErr: commerr.ErrConflict, change: func(_ *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, input *ResumeExecutionInput) uuid.UUID {
			input.ControlRevision++

			return uuid.Nil
		}},
		{name: "foreign target", wantErr: commerr.ErrNotFound, change: func(t *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, _ *ResumeExecutionInput) uuid.UUID {
			t.Helper()

			return newExecutionFixture(t).root.ID
		}},
		{name: "foreign generation", wantErr: commerr.ErrPermissionDenied, change: func(_ *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, input *ResumeExecutionInput) uuid.UUID {
			input.SourceLease.GenerationID = uuid.New()

			return uuid.Nil
		}},
		{name: "stale attempt", wantErr: commerr.ErrConflict, change: func(_ *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, input *ResumeExecutionInput) uuid.UUID {
			input.SourceLease.Attempt++

			return uuid.Nil
		}},
		{name: "missing lease", wantErr: commerr.ErrValidationFailed, change: func(_ *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, input *ResumeExecutionInput) uuid.UUID {
			input.SourceLease = ActivityLease{}

			return uuid.Nil
		}},
		{name: "negative revision", wantErr: commerr.ErrValidationFailed, change: func(_ *testing.T, _ executionFixture, _ *models.Execution, _ *ActivityClaim, input *ResumeExecutionInput) uuid.UUID {
			input.ControlRevision = -1

			return uuid.Nil
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, target, claim := agentResumeFixture(t)
			input := ResumeExecutionInput{SourceLease: claim.Lease, ControlRevision: 1}
			id := tc.change(t, fixture, target, claim, &input)
			if id == uuid.Nil {
				id = target.ID
			}
			_, err := fixture.store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, id, input)
			require.ErrorIs(t, err, tc.wantErr)
			stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, target.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStatePaused, stored.State)
			assert.EqualValues(t, 1, stored.ControlRevision)
		})
	}
}
