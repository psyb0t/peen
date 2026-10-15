package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFinalAcceptanceFixture(
	t *testing.T,
	driver *elelemtest.ScriptedDriver,
	maxRounds int,
	arrivals int32,
) executionLoopFixture {
	t.Helper()
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	fixture.input.MaxRounds = maxRounds
	var checks atomic.Int32
	fixture.engine.options.Store = &finishAcceptanceStore{
		ExecutionStorage: fixture.base.store,
		beforeFinish: func(ctx context.Context) error {
			count := checks.Add(1)
			if count > arrivals {
				return nil
			}

			return prepared.userMessages.enqueue(ctx, TurnRequest{
				RequestID: uuid.New(), Message: fmt.Sprintf("pending request %d", count),
			})
		},
	}

	return fixture
}

func TestWorkflowFinalContinuationKeepsRoundBudget(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("last answer"),
		elelemtest.Text("must not request"))
	fixture := newFinalAcceptanceFixture(t, driver, 1, 1)
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.Error(t, err)
	assert.Equal(t, 1, driver.Calls())
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
	fixture.stop()
}

func TestWorkflowRepeatedFinalArrivalsUseDistinctDecisions(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("first"),
		elelemtest.Text("second"), elelemtest.Text("third"))
	fixture := newFinalAcceptanceFixture(t, driver, 3, 2)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "third", result.Text)
	assert.Equal(t, 3, driver.Calls())
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	for _, message := range page.Items {
		assert.Equal(t, models.InboxMessageStateDelivered, message.State)
	}

	activities, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	var steps []string
	for _, activity := range activities.Items {
		if activity.Name == session.ExecutionFinishActivityName {
			steps = append(steps, activity.StepID)
		}
	}
	assert.Equal(t, []string{"finish", "finish:1", "finish:2"}, steps)
	fixture.stop()
}

func TestWorkflowFinishContinuesAfterRecordedModelResult(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("first answer"),
		elelemtest.Text("continued answer"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	var once sync.Once
	fixture.engine.options.Store = &finishAcceptanceStore{
		ExecutionStorage: fixture.base.store,
		beforeFinish: func(ctx context.Context) error {
			var err error
			once.Do(func() {
				err = prepared.userMessages.enqueue(ctx, TurnRequest{
					RequestID: uuid.New(), Message: "accepted after model checkpoint",
				})
			})

			return err
		},
	}
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "continued answer", result.Text)
	assert.Equal(t, 2, driver.Calls())
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	fixture.stop()
}
