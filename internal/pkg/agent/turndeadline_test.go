package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeTurnDeadlineCancels(t *testing.T) {
	t.Parallel()
	base := newNativeRunnerFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("deadlineCall", nativeRunnerBlockingTool, `{}`)))
	base.runtime.extraTools = []elelem.Tool{{
		Name: nativeRunnerBlockingTool, ArgumentsSchema: json.RawMessage(`{}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (elelem.ToolResult, error) {
			<-ctx.Done()

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(), "stop deadline tool")
		},
	}}
	_, err := base.runtime.RunMessage(t.Context(), MessageRequest{
		Message: "wait for the deadline", TimeoutSeconds: 1,
	}, uuid.New(), nil)
	require.ErrorIs(t, err, ErrTurnTimeout)
	page, err := base.store.ListTurns(t.Context(), base.runtime.sessionID,
		session.ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.TurnStateCancelled, page.Items[0].State)
	assert.True(t, page.Items[0].CancelRequested)
	assert.Equal(t, "timeout", page.Items[0].FailureClassification)
	events, err := base.store.ListEvents(t.Context(), base.runtime.sessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	for _, event := range events.Items {
		if event.EventType == EventTypeTurnCancelled {
			assert.JSONEq(t, `{"reason":"timeout"}`, event.PayloadJSON)

			return
		}
	}

	t.Fatal("timeout terminal event is missing")
}

func TestCompletedTurnDisarmsDeadline(t *testing.T) {
	t.Parallel()
	const timeout = 30 * time.Second
	deadline := time.Now().UTC().Add(timeout)
	base := newNativeRunnerFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("finished")))
	_, err := base.runtime.RunMessage(t.Context(), MessageRequest{
		Message: "finish promptly", TimeoutSeconds: int(timeout / time.Second),
	}, uuid.New(), nil)
	require.NoError(t, err)
	before, err := base.store.Get(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)
	select {
	case <-time.After(time.Until(deadline) + time.Second):
	case <-t.Context().Done():
		t.Fatal("test ended before deadline window")
	}

	after, err := base.store.Get(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)
	assert.Equal(t, before.StopRevision, after.StopRevision)
}

func TestTurnBootstrapRetainsDeadlineAndSchema(t *testing.T) {
	t.Parallel()
	deadline := time.Now().UTC().Add(-time.Second)
	saved := workflowRootBootstrap{
		Version:      rootWorkflowBootstrapVersion,
		OutputSchema: json.RawMessage(testOutputSchema), Deadline: deadline,
	}
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	var restored workflowRootBootstrap
	require.NoError(t, json.Unmarshal(encoded, &restored))
	request := restored.request(nil)
	assert.Equal(t, deadline, request.Deadline)
	assert.JSONEq(t, testOutputSchema, string(request.OutputSchema))
	assert.True(t, restored.hasSupportedVersion())
	var legacy workflowRootBootstrap
	require.NoError(t, json.Unmarshal([]byte(`{"version":1}`), &legacy))
	assert.True(t, legacy.hasSupportedVersion())
	assert.True(t, legacy.request(nil).Deadline.IsZero())
}

func TestRestoredTurnWithExpiredDeadlineCancelsImmediately(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(original.lease)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Context, &conversation))
	conversation.Root.Deadline = time.Now().UTC().Add(-time.Second)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Context = encoded
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.NoError(t, err)
	fixture.base.runtime.armTurnDeadline(t.Context(), restored)
	t.Cleanup(restored.stopDeadline)
	require.EventuallyWithT(t, func(check *assert.CollectT) {
		page, readErr := fixture.base.store.ListTurns(t.Context(),
			input.SessionID, session.ListTurnsOptions{})
		if !assert.NoError(check, readErr) || !assert.Len(check, page.Items, 1) {
			return
		}

		assert.Equal(check, models.TurnStateCancelled, page.Items[0].State)
		assert.Equal(check, "timeout", page.Items[0].FailureClassification)
	}, workflowTestResultTimeout, 10*time.Millisecond)
}
