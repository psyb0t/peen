package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	turnEndTestSystemPrompt = "you are a test agent"
	turnEndTestAnswered     = "call-answered"
	turnEndTestUnanswered   = "call-unanswered"
)

func turnEndTestNotice(eventType, source string) events.Notice {
	return events.Notice{ID: uuid.New(), Type: eventType, Source: source}
}

// The model never reads a turn-end notice as event data: the ended turn's
// own messages and its system message carry it. Other notices still reach it.
func TestRenderModelEventsLeavesOutTurnEndNotices(t *testing.T) {
	t.Parallel()

	turnFailed := turnEndTestNotice(EventTypeTurnFailed,
		session.TurnEndNoticeSource)
	turnCancelled := turnEndTestNotice(EventTypeTurnCancelled,
		session.TurnEndNoticeSource)
	jobExited := turnEndTestNotice("job.exited", "jobs")

	testCases := []struct {
		name       string
		batch      events.Batch
		wantIDs    []uuid.UUID
		wantNoText bool
	}{
		{
			name:       "only turn-end notices",
			batch:      events.Batch{Notices: []events.Notice{turnFailed, turnCancelled}},
			wantNoText: true,
		},
		{
			name: "a turn-end notice beside another notice",
			batch: events.Batch{
				Notices: []events.Notice{turnFailed, jobExited},
			},
			wantIDs: []uuid.UUID{jobExited.ID},
		},
		{
			name: "dropped notices are still reported",
			batch: events.Batch{
				Notices: []events.Notice{turnFailed}, Dropped: 2,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text := renderModelEvents(tc.batch)
			if tc.wantNoText {
				assert.Empty(t, text)

				return
			}

			require.NotEmpty(t, text)
			assert.NotContains(t, text, turnFailed.ID.String())
			assert.NotContains(t, text, turnCancelled.ID.String())

			for _, id := range tc.wantIDs {
				assert.Contains(t, text, id.String())
			}
		})
	}
}

func historyRow(
	role models.MessageRole,
	sequence int64,
	turnID uuid.UUID,
) *models.Message {
	return &models.Message{
		ID: uuid.New(), Role: role, Sequence: sequence, TurnID: turnID,
		ToolCallsJSON: "[]",
	}
}

func historyCalls(t *testing.T, ids ...string) string {
	t.Helper()

	calls := make([]elelem.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, elelem.ToolCall{
			ID: id, Name: "readFile",
			Arguments: json.RawMessage(`{"path":"notes.txt"}`),
		})
	}

	encoded, err := json.Marshal(calls)
	require.NoError(t, err)

	return string(encoded)
}

// A failed turn's reply keeps only the calls that got a result, so the
// replayed history has no unanswered call, and the plan still maps one model
// message to each stored row.
func TestPromptFromHistoryReplaysAnUnfinishedTurn(t *testing.T) {
	t.Parallel()

	failedTurn := uuid.New()

	prompt := historyRow(models.MessageRoleUser, 1, failedTurn)
	prompt.Content = "rename the tool"
	prompt.Incomplete = true

	reply := historyRow(models.MessageRoleAssistant, 2, failedTurn)
	reply.ToolCallsJSON = historyCalls(t, turnEndTestAnswered,
		turnEndTestUnanswered)
	reply.Incomplete = true

	result := historyRow(models.MessageRoleTool, 3, failedTurn)
	result.ToolCallID = turnEndTestAnswered
	result.Content = "contents"
	result.Incomplete = true

	ended := historyRow(models.MessageRoleSystem, 4, failedTurn)
	ended.Content = "This turn did not finish. It failed."
	ended.Injected = true

	built, plan, err := promptFromHistory(turnEndTestSystemPrompt,
		&session.History{
			Messages: []*models.Message{prompt, reply, result, ended},
		})
	require.NoError(t, err)

	messages := built.Messages()
	require.Len(t, messages, 5)
	require.NoError(t, plan.verify(messages))

	assert.Equal(t, elelem.RoleUser, messages[1].Role)
	assert.Equal(t, elelem.RoleAssistant, messages[2].Role)
	require.Len(t, messages[2].ToolCalls, 1)
	assert.Equal(t, turnEndTestAnswered, messages[2].ToolCalls[0].ID)
	assert.Equal(t, elelem.RoleTool, messages[3].Role)
	assert.Equal(t, elelem.RoleSystem, messages[4].Role)
	assert.Equal(t, ended.Content, messages[4].Text())
}

// A workflow keeps its system prompt apart from its history, so only the
// leading system message goes. A later one is part of the conversation.
func TestDirectoryHistoryKeepsLaterSystemMessages(t *testing.T) {
	t.Parallel()

	prompt := elelem.Message{
		Role: elelem.RoleSystem, Content: elelem.Text("system prompt"),
	}
	request := elelem.Message{
		Role: elelem.RoleUser, Content: elelem.Text("rename it"),
	}
	ended := elelem.Message{
		Role: elelem.RoleSystem, Content: elelem.Text("turn ended"),
	}

	testCases := []struct {
		name     string
		messages []elelem.Message
		want     []elelem.Message
	}{
		{name: "empty", messages: nil, want: []elelem.Message{}},
		{
			name:     "leading system prompt",
			messages: []elelem.Message{prompt, request, ended},
			want:     []elelem.Message{request, ended},
		},
		{
			name:     "no system prompt",
			messages: []elelem.Message{request, ended},
			want:     []elelem.Message{request, ended},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := directoryHistory(tc.messages)
			if len(tc.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// A result stored after the turn's system message, such as one a crashed
// call received once it was retried, does not answer the call in the
// replayed history: the call is left out and the result becomes a system
// message, so every remaining call is followed directly by its result.
func TestPromptFromHistoryReplaysALateToolResult(t *testing.T) {
	t.Parallel()

	crashedTurn := uuid.New()
	laterTurn := uuid.New()

	prompt := historyRow(models.MessageRoleUser, 1, crashedTurn)
	prompt.Content = "run the command"

	reply := historyRow(models.MessageRoleAssistant, 2, crashedTurn)
	reply.ToolCallsJSON = historyCalls(t, turnEndTestUnanswered)
	reply.Incomplete = true

	ended := historyRow(models.MessageRoleSystem, 3, crashedTurn)
	ended.Content = "This turn did not finish."

	question := historyRow(models.MessageRoleUser, 4, laterTurn)
	question.Content = "inspect it"

	answer := historyRow(models.MessageRoleAssistant, 5, laterTurn)
	answer.Content = "inspected"

	late := historyRow(models.MessageRoleTool, 6, crashedTurn)
	late.ToolCallID = turnEndTestUnanswered
	late.Content = "effect"
	late.IsError = true

	built, plan, err := promptFromHistory(turnEndTestSystemPrompt,
		&session.History{Messages: []*models.Message{
			prompt, reply, ended, question, answer, late,
		}})
	require.NoError(t, err)

	messages := built.Messages()
	require.Len(t, messages, 7)
	require.NoError(t, plan.verify(messages))

	assert.Empty(t, messages[2].ToolCalls)
	assert.Equal(t, unansweredReplyText, messages[2].Text())
	assert.Equal(t, elelem.RoleSystem, messages[6].Role)
	assert.Empty(t, messages[6].ToolCallID)
	assert.False(t, messages[6].ToolResultIsError)
	assert.Contains(t, messages[6].Text(), "readFile")
	assert.Contains(t, messages[6].Text(), turnEndTestUnanswered)
	assert.Contains(t, messages[6].Text(), "effect")
}

func TestPairToolResults(t *testing.T) {
	t.Parallel()

	turnID := uuid.New()
	reply := func(text string, ids ...string) *models.Message {
		message := historyRow(models.MessageRoleAssistant, 1, turnID)
		message.Content = text
		message.ToolCallsJSON = historyCalls(t, ids...)

		return message
	}
	result := func(sequence int64, id string) *models.Message {
		message := historyRow(models.MessageRoleTool, sequence, turnID)
		message.ToolCallID = id

		return message
	}
	ended := historyRow(models.MessageRoleSystem, 3, turnID)

	testCases := []struct {
		name      string
		history   []*models.Message
		wantRoles []models.MessageRole
		wantCalls []string
		wantText  string
	}{
		{
			name: "keeps every answered call",
			history: []*models.Message{
				reply("", turnEndTestAnswered, turnEndTestUnanswered),
				result(2, turnEndTestUnanswered),
				result(3, turnEndTestAnswered),
			},
			wantRoles: []models.MessageRole{
				models.MessageRoleAssistant, models.MessageRoleTool,
				models.MessageRoleTool,
			},
			wantCalls: []string{turnEndTestAnswered, turnEndTestUnanswered},
		},
		{
			name: "leaves out the call with no result",
			history: []*models.Message{
				reply("", turnEndTestAnswered, turnEndTestUnanswered),
				result(2, turnEndTestAnswered), ended,
			},
			wantRoles: []models.MessageRole{
				models.MessageRoleAssistant, models.MessageRoleTool,
				models.MessageRoleSystem,
			},
			wantCalls: []string{turnEndTestAnswered},
		},
		{
			name: "keeps the reply text when every call is left out",
			history: []*models.Message{
				reply("reading it", turnEndTestUnanswered), ended,
			},
			wantRoles: []models.MessageRole{
				models.MessageRoleAssistant, models.MessageRoleSystem,
			},
			wantCalls: []string{},
			wantText:  "reading it",
		},
		{
			name: "an empty reply gets a stand-in text",
			history: []*models.Message{
				reply("", turnEndTestUnanswered), ended,
			},
			wantRoles: []models.MessageRole{
				models.MessageRoleAssistant, models.MessageRoleSystem,
			},
			wantCalls: []string{},
			wantText:  unansweredReplyText,
		},
		{
			name: "a result after the system message becomes a system message",
			history: []*models.Message{
				reply("", turnEndTestUnanswered), ended,
				result(4, turnEndTestUnanswered),
			},
			wantRoles: []models.MessageRole{
				models.MessageRoleAssistant, models.MessageRoleSystem,
				models.MessageRoleSystem,
			},
			wantCalls: []string{},
			wantText:  unansweredReplyText,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			paired, err := pairToolResults(tc.history)
			require.NoError(t, err)
			require.Len(t, paired, len(tc.history))

			roles := make([]models.MessageRole, 0, len(paired))
			for index, message := range paired {
				roles = append(roles, message.Role)
				assert.Equal(t, tc.history[index].ID, message.ID)
			}

			assert.Equal(t, tc.wantRoles, roles)

			var calls []elelem.ToolCall
			require.NoError(t, json.Unmarshal(
				[]byte(paired[0].ToolCallsJSON), &calls))

			ids := make([]string, 0, len(calls))
			for _, call := range calls {
				ids = append(ids, call.ID)
			}

			assert.Equal(t, tc.wantCalls, ids)
			assert.Equal(t, tc.wantText, paired[0].Content)
		})
	}
}
