package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentInspectionControlOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	step := session.ScheduleActivityInput{StepID: "unknownTool", Name: "unknownTool", Kind: models.ActivityKindTool, ReplayPolicy: models.ActivityReplayPolicyReconcile, Input: json.RawMessage(`{}`)}
	unknown, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID, child.ID, fixture.controller.generationID, step)
	require.NoError(t, err)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID, child.ID, rpcPauseReason)
	require.NoError(t, err)
	require.NoError(t, remote.InterruptExecutionActivity(t.Context(), fixture.sessionID, unknown.Lease))
	step.StepID = "inspectChild"
	source, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID, *child.ParentExecutionID, fixture.controller.generationID, step)
	require.NoError(t, err)
	input := session.ResolveExecutionControlInput{
		SourceLease: source.Lease, ControlRevision: 1,
		ResolveActivityInput: session.ResolveActivityInput{Attempt: unknown.Lease.Attempt, Resolution: session.ActivityResolutionRetry, Evidence: "inspected the saved effect"},
	}
	forged := input
	forged.SourceLease.GenerationID = uuid.New()
	_, err = remote.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, unknown.Activity.ID, forged)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.ResolveExecutionActivityFromActivity(t.Context(), uuid.New(), unknown.Activity.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	resolved, err := remote.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, unknown.Activity.ID, input)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateScheduled, resolved.State)
	stored, err := remote.GetExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.EqualValues(t, 1, stored.ControlRevision)
}
