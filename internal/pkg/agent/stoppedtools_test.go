package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	stoppedToolName   = "stoppedToolBlock"
	stoppedToolCallID = "stoppedToolCall"
)

func stoppedBlockingTools(started chan<- struct{}) []elelem.Tool {
	return []elelem.Tool{{
		Name:            stoppedToolName,
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			close(started)
			<-ctx.Done()

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(),
				"interrupt stopped test operation")
		},
	}}
}

// stopRunningTurn starts a turn whose only tool blocks until it is stopped,
// stops the session, and waits for the turn to return.
func stopRunningTurn(t *testing.T, base runtimeFixture) {
	t.Helper()

	started := make(chan struct{})
	base.runtime.extraTools = stoppedBlockingTools(started)

	finished := runNativeTurnAsync(t.Context(), base.runtime)

	select {
	case <-started:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("the blocking tool did not start")
	}

	_, err := base.runtime.CancelSession(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)

	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("the stopped turn did not return")
	}
}

// assertStoppedToolClosed checks the stored events of a stopped turn: the
// tool.use is followed by a tool.result for the same call that marks an
// error, then by turn.cancelled, and the transcript holds the tool message.
func assertStoppedToolClosed(t *testing.T, base runtimeFixture) {
	t.Helper()

	page, err := base.store.ListEvents(t.Context(), base.runtime.sessionID,
		session.ListEventsOptions{
			EventTypes: []string{
				EventTypeToolUse, EventTypeToolResult, EventTypeTurnCancelled,
			},
			Limit: session.MaximumPageLimit,
		})
	require.NoError(t, err)

	types := make([]string, 0, len(page.Items))
	for _, event := range page.Items {
		types = append(types, event.EventType)
	}

	require.Equal(t, []string{
		EventTypeToolUse, EventTypeToolResult, EventTypeTurnCancelled,
	}, types)

	var use, result struct {
		CallID  string `json:"callId"`
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	}

	require.NoError(t, json.Unmarshal([]byte(page.Items[0].PayloadJSON), &use))
	require.NoError(t, json.Unmarshal([]byte(page.Items[1].PayloadJSON),
		&result))
	assert.Equal(t, stoppedToolCallID, use.CallID)
	assert.Equal(t, stoppedToolCallID, result.CallID)
	assert.True(t, result.IsError)
	assert.Equal(t, session.StoppedToolResultContent, result.Content)

	messages, err := base.store.ListMessages(t.Context(),
		base.runtime.sessionID, session.ListMessagesOptions{
			Limit: session.MaximumPageLimit, Order: session.PageOrderAscending,
		})
	require.NoError(t, err)

	var tools []*models.Message

	for _, message := range messages.Items {
		if message.Role == models.MessageRoleTool {
			tools = append(tools, message)
		}
	}

	require.Len(t, tools, 1)
	assert.Equal(t, stoppedToolCallID, tools[0].ToolCallID)
	assert.True(t, tools[0].IsError)
	assert.Equal(t, session.StoppedToolResultContent, tools[0].Content)
}

//nolint:ireturn // The runtime fixtures take the elelem.Driver contract.
func stoppedToolDriver() elelem.Driver {
	return elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(stoppedToolCallID, stoppedToolName, `{}`),
		elelemtest.Text("unreached"))
}

// A tool call that was running when the turn was stopped gets a tool.result
// before the turn's terminal frame, on the in-process turn path.
func TestStoppedTurnClosesItsRunningToolCall(t *testing.T) {
	t.Parallel()

	base := newRuntimeFixtureWithOptions(t, stoppedToolDriver(), nil)

	stopRunningTurn(t, base)
	assertStoppedToolClosed(t, base)
}

// The same holds when the turn runs as a durable workflow root, which ends
// the stopped turn from the store.
func TestStoppedWorkflowTurnClosesItsRunningToolCall(t *testing.T) {
	t.Parallel()

	base := newNativeRunnerFixture(t, stoppedToolDriver())

	stopRunningTurn(t, base)
	assertStoppedToolClosed(t, base)
}

// A stopped tool call is not a real result, so the notice for the ended turn
// still reports the call as having none.
func TestStoppedToolCallStaysNoResultInTheTurnEndNotice(t *testing.T) {
	t.Parallel()

	base := newNativeRunnerFixture(t, stoppedToolDriver())

	stopRunningTurn(t, base)

	notices, err := base.store.ListSessionNotices(t.Context(),
		base.runtime.sessionID, session.ListSessionNoticesOptions{
			Limit: session.MaximumPageLimit,
		})
	require.NoError(t, err)

	var found bool

	for _, notice := range notices.Items {
		if notice.Type != EventTypeTurnCancelled {
			continue
		}

		var data session.TurnEndNotice

		require.NoError(t, json.Unmarshal([]byte(notice.DataJSON), &data))
		require.Len(t, data.ToolCalls, 1)
		assert.Equal(t, stoppedToolName, data.ToolCalls[0].Name)
		assert.Equal(t, "noResult", data.ToolCalls[0].Outcome)

		found = true
	}

	assert.True(t, found, "the stopped turn must leave a cancelled notice")
}

// A child stopped with a tool call running gets an agent.run.tool.result
// before agent.run.cancelled.
func TestStoppedChildClosesItsRunningToolCall(t *testing.T) {
	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	registry, run, _, sink := startDurableAgentRun(
		context.Background(),
		t,
		fixture,
	)

	require.NoError(t, sink.onToolCallStart(context.Background(),
		elelem.ToolCallEvent{
			CallID: stoppedToolCallID, Name: stoppedToolName,
			Arguments: json.RawMessage(`{}`),
		}))

	result := fixture.runtime.finishAgentRun(
		context.Background(),
		registry,
		sink,
		run,
		nil,
		context.Canceled,
	)
	assert.Equal(t, AgentRunStateCancelled, result.State)

	page, err := fixture.store.ListAgentRunEvents(context.Background(),
		run.SessionID, run.ID, session.ListAgentRunEventsOptions{
			Limit: session.MaximumPageLimit,
		})
	require.NoError(t, err)

	types := make([]string, 0, len(page.Items))

	for _, event := range page.Items {
		types = append(types, event.EventType)
	}

	require.Equal(t, []string{
		EventTypeAgentRunToolUse, EventTypeAgentRunToolResult,
		EventTypeAgentRunCancelled,
	}, types)

	var payload toolResultPayload

	require.NoError(t, json.Unmarshal([]byte(page.Items[1].PayloadJSON),
		&payload))
	assert.Equal(t, stoppedToolCallID, payload.CallID)
	assert.Equal(t, stoppedToolName, payload.Name)
	assert.True(t, payload.IsError)
	assert.Equal(t, session.StoppedToolResultContent, payload.Content)

	messages, err := fixture.store.ListAgentRunMessages(
		context.Background(), run.SessionID, run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit})
	require.NoError(t, err)

	var toolMessages int

	for _, message := range messages.Items {
		if message.Role == models.MessageRoleTool {
			toolMessages++

			assert.Equal(t, stoppedToolCallID, message.ToolCallID)
			assert.True(t, message.IsError)
		}
	}

	assert.Equal(t, 1, toolMessages)
}
