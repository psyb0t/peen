package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootWorkflowStreamClosesPartialBlockBeforeResume(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("partial attempt"), elelemtest.Text("completed attempt"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	t.Cleanup(finish)
	var firstDelta sync.Once
	prepared.turn.sink = func(event Event) error {
		if event.Type == essessey.EventTypeContentBlockDelta {
			isFirst := false
			firstDelta.Do(func() {
				isFirst = true
				close(entered)
				<-release
			})
			if isFirst {
				return context.Canceled
			}
		}

		return nil
	}
	instance := fixture.start(t)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model stream did not reach interruption gate")
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave partial model request paused"))
	finish()
	require.Eventually(t, func() bool {
		runs, err := fixture.base.store.ListModelRuns(t.Context(),
			fixture.input.SessionID, session.ListModelRunsOptions{})

		return err == nil && len(runs.Items) == 1 &&
			runs.Items[0].State == models.ModelRunStateCancelled
	}, 10*time.Second, 10*time.Millisecond)
	_, err := fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(), instance.InstanceID,
		workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "completed attempt", result.Text)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0].Messages, requests[1].Messages)
	events, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	var starts, stops []int
	for _, event := range events.Items {
		if event.EventType != essessey.EventTypeContentBlockStart &&
			event.EventType != essessey.EventTypeContentBlockStop {
			continue
		}

		var block struct {
			Index int `json:"index"`
		}
		require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &block))
		if event.EventType == essessey.EventTypeContentBlockStart {
			starts = append(starts, block.Index)
		} else {
			stops = append(stops, block.Index)
		}
	}
	assert.Equal(t, []int{0, 1}, starts)
	assert.Equal(t, starts, stops)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 2)
	assert.Equal(t, "completed attempt", messages.Items[1].Content)
	fixture.stop()
}

func TestRootWorkflowMonitorLeavesClosedStreamsUntouched(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		streamStarted   bool
		assistantClosed bool
		cause           error
	}{
		{name: "success"},
		{name: "before start", cause: context.Canceled},
		{
			name: "already closed", cause: context.Canceled,
			streamStarted: true, assistantClosed: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			monitor := rootWorkflowModelMonitor{
				streamStarted:   tc.streamStarted,
				assistantClosed: tc.assistantClosed,
			}
			err := monitor.closeInterruptedStream(t.Context(), nil, tc.cause)
			require.ErrorIs(t, err, tc.cause)
		})
	}
}
