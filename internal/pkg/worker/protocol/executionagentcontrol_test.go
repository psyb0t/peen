package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentResumeControlOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	_, err := remote.PauseExecution(t.Context(), fixture.sessionID, child.ID, rpcPauseReason)
	require.NoError(t, err)
	root, err := remote.GetExecution(t.Context(), fixture.sessionID, *child.ParentExecutionID)
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID, root.ID, fixture.controller.generationID, session.ScheduleActivityInput{
		StepID: "resumeChild", Name: "resumeExecution", Kind: models.ActivityKindTool, ReplayPolicy: models.ActivityReplayPolicyReconcile, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	input := session.ResumeExecutionInput{SourceLease: claim.Lease, ControlRevision: 1}
	forged := input
	forged.SourceLease.GenerationID = uuid.New()
	_, err = remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, child.ID, forged)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.ResumeExecutionFromActivity(t.Context(), uuid.New(), child.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	changed, err := remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{child.ID}, changed)
	stored, err := remote.GetExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateQueued, stored.State)
	assert.EqualValues(t, 2, stored.ControlRevision)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID, child.ID, rpcPauseReason)
	require.NoError(t, err)
	_, err = remote.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	input.ControlRevision = 3
	_, err = remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, child.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	stored, err = remote.GetExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.EqualValues(t, 3, stored.ControlRevision)
}

func TestDeferredResumeSelectionReachesAuthenticatedStorage(t *testing.T) {
	t.Parallel()
	fixture, remote, root := newExecutionRPCFixture(t)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID, root.ID, fixture.controller.generationID, session.ScheduleActivityInput{
		StepID: "resumeDeferred", Name: "resumeExecution", Kind: models.ActivityKindTool, ReplayPolicy: models.ActivityReplayPolicyReconcile, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	requestID, revision := uuid.New(), int64(0)
	input := session.ResumeExecutionInput{SourceLease: claim.Lease, DeferredRequestID: &requestID, DeferredStopRevision: &revision}
	_, err = remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, root.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.DeferredStopRevision = nil
	_, err = remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, root.ID, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	input.SourceLease.GenerationID = uuid.New()
	_, err = remote.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, root.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	saved, err := remote.GetExecution(t.Context(), fixture.sessionID, root.ID)
	require.NoError(t, err)
	assert.Zero(t, saved.ControlRevision)
}

func TestQuestionWakeSourceValidationOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	data, err := json.Marshal(events.AgentNoticeData{RunID: *child.AgentRunID, Message: "child question"})
	require.NoError(t, err)
	notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID, session.CreateSessionNoticeInput{
		Type: events.TypeAgentQuestion, Source: events.AgentSource, Summary: "child question", DataJSON: string(data), Delivery: models.NoticeDeliveryWake,
	})
	require.NoError(t, err)
	options := session.ListSessionNoticesOptions{ID: &notice.ID, ValidateWakeSource: true}
	page, err := remote.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
	_, err = remote.ListSessionNotices(t.Context(), uuid.New(), options)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID, child.ID, rpcPauseReason)
	require.NoError(t, err)
	_, err = remote.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	options.ID = nil
	_, err = remote.ListSessionNotices(t.Context(), fixture.sessionID, options)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
