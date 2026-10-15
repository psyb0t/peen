package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeOpeningHooksHaveCheckpointBeforeEffects(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("opened"))
	base := newNativeRunnerFixture(t, driver)
	var mutex sync.Mutex
	var events []harness.HookEvent
	base.runtime.hookCallback = func(ctx context.Context, input hooks.Invocation) (hooks.Outcome, error) {
		if input.Event != harness.HookEventPreUserMessage && input.Event != harness.HookEventSessionStart &&
			input.Event != harness.HookEventPostUserMessage && input.Event != harness.HookEventTurnStart {
			return hooks.Outcome{}, nil
		}
		page, err := base.store.SearchExecutions(ctx, input.SessionID, session.SearchExecutionsOptions{})
		if err != nil {
			return hooks.Outcome{}, ctxerrors.Wrap(err, "read opening execution")
		}
		if len(page.Items) != 1 {
			return hooks.Outcome{}, ctxerrors.Wrap(commerr.ErrInvalidState, "opening hook ran before execution existed")
		}
		activities, err := base.store.ListExecutionActivities(ctx, input.SessionID, page.Items[0].ID, session.ListEventsOptions{})
		if err != nil {
			return hooks.Outcome{}, ctxerrors.Wrap(err, "read opening checkpoints")
		}
		found := false
		for _, activity := range activities.Items {
			if activity.StepID == "opening:"+string(input.Event) {
				found = activity.State == models.ActivityStateRunning && activity.ReplayPolicy == models.ActivityReplayPolicyReconcile
			}
		}
		if !found {
			return hooks.Outcome{}, ctxerrors.Wrap(commerr.ErrInvalidState, "opening hook ran without an effect checkpoint")
		}
		mutex.Lock()
		events = append(events, input.Event)
		mutex.Unlock()

		return hooks.Outcome{Injections: []string{fmt.Sprintf("saved %s instruction", input.Event)}}, nil
	}
	result, err := base.runtime.Run(t.Context(), TurnRequest{RequestID: uuid.New(), Message: "open workflow"})
	require.NoError(t, err)
	assert.Equal(t, "opened", result.Text)
	mutex.Lock()
	assert.Equal(t, []harness.HookEvent{
		harness.HookEventPreUserMessage, harness.HookEventSessionStart,
		harness.HookEventPostUserMessage, harness.HookEventTurnStart,
	}, events)
	mutex.Unlock()
	requests := driver.Requests()
	require.Len(t, requests, 1)
	for _, event := range events {
		assert.Contains(t, requests[0].Messages[0].Text(), fmt.Sprintf("saved %s instruction", event))
	}
}

func TestNativeOpeningDenialCommitsWithoutCallingModel(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver()
	base := newNativeRunnerFixture(t, driver)
	base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
		if input.Event == harness.HookEventPreUserMessage {
			return hooks.Outcome{}, &hooks.DenialError{Reason: "fixture opening denial"}
		}

		return hooks.Outcome{}, nil
	}
	_, err := base.runtime.Run(t.Context(), TurnRequest{RequestID: uuid.New(), Message: "denied opening"})
	require.Error(t, err)
	assert.Zero(t, driver.Calls())
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ExecutionStateFailed, page.Items[0].State)
	activities, err := base.store.ListExecutionActivities(t.Context(), base.runtime.sessionID, page.Items[0].ID, session.ListEventsOptions{})
	require.NoError(t, err)
	var opening *models.ExecutionActivity
	for _, activity := range activities.Items {
		if activity.Name == openingHookActivityName {
			require.Nil(t, opening, "later opening hooks must not run after denial")
			opening = activity
		}
	}
	require.NotNil(t, opening)
	assert.Equal(t, models.ActivityStateCompleted, opening.State)
	require.NotNil(t, opening.OutputJSON)
	assert.Contains(t, *opening.OutputJSON, "fixture opening denial")
}

func TestOpeningProjectionChecksCommittedInputAndOutput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                                         string
		changedInput, changedOutput, missingActivity bool
		wantErr                                      error
	}{
		{name: "unchanged"},
		{name: "input", changedInput: true, wantErr: commerr.ErrConflict},
		{name: "output", changedOutput: true, wantErr: commerr.ErrConflict},
		{name: "missing", missingActivity: true, wantErr: commerr.ErrNotFound},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, callbacks, _ := rootModelProjectionFixture(t)
			input := workflowOpeningInput{Execution: fixture.input, Event: harness.HookEventPreUserMessage}
			result, err := fixture.engine.openingHook(t.Context(), input)
			require.NoError(t, err)
			if tc.changedInput {
				input.Event = harness.HookEventTurnStart
			}
			if tc.changedOutput {
				result.Output.Failure = "changed saved result"
			}
			if tc.missingActivity {
				result.Output.RootProjection.ActivityID = uuid.New()
			}
			err = callbacks.afterOpeningHook(t.Context(), input, result.Output)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
