package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// turnCompactionTestRounds and turnCompactionTestContextTokens are tuned
	// for the fixture's 100 tokens per message: summarizing every ended turn
	// still leaves the request over budget, and summarizing the first five
	// rounds of the current turn brings it under.
	turnCompactionTestRounds        = 6
	turnCompactionTestContextTokens = 1200
	turnCompactionTestCallFormat    = "call-%d"
	turnCompactionResultFormat      = "round %d result"
)

func turnCompactionRound(round int) []elelem.Message {
	callID := fmt.Sprintf(turnCompactionTestCallFormat, round)

	return []elelem.Message{
		{
			Role: elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{{
				ID: callID, Name: toolNameReadFile,
				Arguments: json.RawMessage(`{}`),
			}},
		},
		{
			Role:       elelem.RoleTool,
			ToolCallID: callID,
			Content: elelem.Text(
				fmt.Sprintf(turnCompactionResultFormat, round)),
		},
	}
}

func TestNativeRootCompactionSummarizesRoundsOfTheCurrentTurn(t *testing.T) {
	t.Parallel()
	fixture, prepared, driver := nativeRootCompactionFixture(t)

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))

	for round := 1; round <= turnCompactionTestRounds; round++ {
		conversation.Messages = append(conversation.Messages,
			turnCompactionRound(round)...)
	}

	conversation.MaxContextTokens = turnCompactionTestContextTokens
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded

	result, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	assert.Nil(t, result.Output.Compaction)
	assert.Equal(t, 4, driver.Calls())

	requests := driver.Requests()
	require.Len(t, requests, 4)

	transcript := requests[3].Messages[1].Text()
	for round := 1; round < turnCompactionTestRounds; round++ {
		assert.Contains(t, transcript,
			fmt.Sprintf(turnCompactionResultFormat, round))
	}

	assert.NotContains(t, transcript,
		fmt.Sprintf(turnCompactionResultFormat, turnCompactionTestRounds))
	assert.NotContains(t, transcript, prepared.input.Message)

	var compacted workflowConversation
	require.NoError(t, json.Unmarshal(result.Output.Context, &compacted))
	require.NoError(t, validateWorkflowCompactionConversation(compacted))

	texts := make([]string, 0, len(compacted.Messages))
	for _, message := range compacted.Messages {
		texts = append(texts, message.Text())
	}

	assert.Contains(t, texts, prepared.input.Message)
	assert.Contains(t, texts, compactionTurnSummaryLead+"retained summary")
	assert.Contains(t, texts,
		fmt.Sprintf(turnCompactionResultFormat, turnCompactionTestRounds))
	assert.NotContains(t, texts, fmt.Sprintf(turnCompactionResultFormat, 1))

	head, err := fixture.base.store.LatestCompaction(t.Context(),
		fixture.input.SessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
}

func TestTurnRoundsStartKeepsTheRequestAndRollsASummary(t *testing.T) {
	t.Parallel()

	user := elelem.Message{Role: elelem.RoleUser, Content: elelem.Text("task")}
	summary := compactionSummaryMessage(compactionTurnSummaryLead + "earlier")
	assistant := elelem.Message{Role: elelem.RoleAssistant}
	tool := elelem.Message{Role: elelem.RoleTool}

	testCases := []struct {
		name      string
		messages  []elelem.Message
		turnStart int
		want      int
	}{
		{
			name:     "skips the opening user messages",
			messages: []elelem.Message{user, user, assistant, tool},
			want:     2,
		},
		{
			name:     "stops at an earlier turn summary",
			messages: []elelem.Message{user, summary, assistant, tool},
			want:     1,
		},
		{
			name:      "clamps a start past the end",
			messages:  []elelem.Message{user},
			turnStart: 3,
			want:      1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, turnRoundsStart(tc.messages, tc.turnStart))
		})
	}
}

func TestTurnRoundEndsKeepCallsWithResultsAndTheNewestRound(t *testing.T) {
	t.Parallel()

	user := elelem.Message{Role: elelem.RoleUser}
	assistant := elelem.Message{Role: elelem.RoleAssistant}
	tool := elelem.Message{Role: elelem.RoleTool}

	testCases := []struct {
		name     string
		messages []elelem.Message
		want     []int
	}{
		{
			name: "three rounds",
			messages: []elelem.Message{
				user, assistant, tool, assistant, tool, tool, assistant, tool,
			},
			want: []int{3, 6},
		},
		{
			name:     "one round",
			messages: []elelem.Message{user, assistant, tool},
			want:     []int{},
		},
		{
			name: "a message after the newest round stays raw",
			messages: []elelem.Message{
				user, assistant, tool, assistant, tool, user,
			},
			want: []int{3},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, turnRoundEnds(tc.messages, 1))
		})
	}
}
