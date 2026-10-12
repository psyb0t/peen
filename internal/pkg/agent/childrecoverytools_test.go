package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveredQuestionDoesNotTakeAnotherCallsAnswer(t *testing.T) {
	t.Parallel()
	fixture, run, driver := recoveryFixture(t)
	ctx := context.Background()
	calls, err := json.Marshal([]elelem.ToolCall{{ID: "wanted", Name: toolNameAsk, Arguments: json.RawMessage(`{"question":"choose a branch","timeout":"1m"}`)}})
	require.NoError(t, err)
	_, err = fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "saved task"},
		{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
	})
	require.NoError(t, err)
	for _, event := range []session.AgentRunEventInput{
		{EventType: EventTypeAgentRunToolUse, PayloadJSON: `{"callId":"wanted","name":"ask"}`},
		{EventType: EventTypeAgentRunQuestion, PayloadJSON: `{"question":"choose a branch","callId":"wanted"}`},
		{EventType: EventTypeAgentRunAnswer, PayloadJSON: `{"answered":false,"callId":"other"}`},
	} {
		_, err := fixture.store.AppendAgentRunEvent(ctx, run.SessionID, run.ID, event)
		require.NoError(t, err)
	}
	questions, err := fixture.store.OpenAgentRunQuestions(ctx, run.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "choose a branch", questions[run.ID])
	restarted := restartRecoveryFixture(t, fixture)
	_, err = restarted.RunMessage(ctx, MessageRequest{SessionID: &run.SessionID, To: &run.ID, Message: "the current answer"}, uuid.New(), nil)
	require.NoError(t, err)
	awaitRecoveredChild(t, fixture, run)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	var toolContent string
	for _, message := range requests[0].Messages {
		if message.ToolCallID == "wanted" {
			toolContent = message.Text()
		}
	}
	assert.JSONEq(t, `{"answered":true}`, toolContent)
}

func TestRecoveredWaitValidatesQuestionInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		tool      string
		arguments string
		wantErr   error
	}{
		{name: "empty question", tool: toolNameAsk, arguments: `{}`, wantErr: commerr.ErrValidationFailed},
		{name: "blank question", tool: toolNameAsk, arguments: `{"question":"  "}`, wantErr: commerr.ErrValidationFailed},
		{name: "question", tool: toolNameAsk, arguments: `{"question":"choose"}`},
		{name: "wait without question", tool: toolNameWaitMessages, arguments: `{}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := recoveredWaitInput(elelem.ToolCall{
				Name: tc.tool, Arguments: json.RawMessage(tc.arguments),
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestRecoveredWaitKeepsTheMessagingToolCap(t *testing.T) {
	t.Parallel()
	fixture, _, _ := recoveryFixture(t)
	testCases := []struct {
		name  string
		tool  string
		input string
		want  time.Duration
	}{
		{name: "unbounded ask", tool: toolNameAsk, want: messageToolTimeout},
		{name: "unbounded wait", tool: toolNameWaitMessages, want: messageToolTimeout},
		{name: "short ask", tool: toolNameAsk, input: "1s", want: time.Second},
		{name: "short wait", tool: toolNameWaitMessages, input: "1s", want: time.Second},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := fixture.runtime.recoveredWaitTimeout(tc.tool, tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
