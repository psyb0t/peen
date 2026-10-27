package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workflowCompactionPrefixFixture(
	t *testing.T,
	completedTurns int,
) (compactionScenario, workflowRoundInput) {
	t.Helper()
	scenario := setupCompactionScenario(t, completedTurns)
	prepared, err := scenario.fixture.runtime.prepareTurn(t.Context(),
		TurnRequest{
			RequestID: uuid.New(), Workspace: scenario.fixture.workspace,
			Message: "continue example work",
		})
	require.NoError(t, err)
	t.Cleanup(func() { scenario.fixture.store.ReleaseTurn(prepared.lease) })
	scope, err := scenario.fixture.runtime.rootDirectoryScope(t.Context(),
		prepared)
	require.NoError(t, err)
	conversation, err := scenario.fixture.runtime.rootWorkflowConversation(
		prepared, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)

	return scenario, workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: prepared.lease.SessionID, ExecutionID: uuid.New(),
		MaxRounds: 4, MaxConcurrentTools: 2, Context: encoded,
	}}
}

func TestWorkflowCompactionPrefixUsesProviderCounter(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 3)
	before := scenario.driver.Calls()
	units, room, err := selectWorkflowCompactionPrefix(
		scenario.fixture.runtime.models, input)
	require.NoError(t, err)
	assert.Equal(t, 4, units)
	assert.Equal(t, compactionTestOutputTokens, room)
	assert.Equal(t, before, scenario.driver.Calls())
}

// A context too full to free the saved summary room gets a summary in a
// smaller room rather than none.
func TestWorkflowCompactionPrefixFallsBackToASmallerSummaryRoom(
	t *testing.T,
) {
	t.Parallel()

	const (
		tokensPerMessage = 1000
		contextTokens    = 7000
		savedRoom        = 4096
	)

	scenario, input := workflowCompactionPrefixFixture(t, 3)
	model, err := scenario.fixture.runtime.models.ResolveModel(
		runtimeTestModelReference)
	require.NoError(t, err)
	model.Client = elelem.New(scenario.driver,
		elelem.WithClientTokenCounter(compactionTokenCounter{
			perMessage: tokensPerMessage,
		}))
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: model,
	})
	require.NoError(t, err)

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.MaxContextTokens = contextTokens
	conversation.OutputReserveTokens = 0
	conversation.Compaction.MaxOutputTokens = savedRoom
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded

	units, room, err := selectWorkflowCompactionPrefix(registry, input)
	require.NoError(t, err)
	assert.Positive(t, units)
	assert.Less(t, room, savedRoom)
	assert.Contains(t, compactionSummaryRooms(savedRoom), room)
}

func TestCompactionSummaryRoomsHalveDownToTheMinimum(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		configured int
		want       []int
	}{
		{
			name:       "halves stop at the minimum",
			configured: 4096,
			want:       []int{4096, 2048, 1024, 512},
		},
		{
			name:       "a room between minimums keeps the last half above",
			configured: 3000,
			want:       []int{3000, 1500, 750},
		},
		{
			name:       "a room below the minimum is the only room",
			configured: 50,
			want:       []int{50},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, compactionSummaryRooms(tc.configured))
		})
	}
}

func TestWorkflowCompactionPrefixSkipsUnderBudget(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 2)
	before := scenario.driver.Calls()
	units, _, err := selectWorkflowCompactionPrefix(
		scenario.fixture.runtime.models, input)
	require.NoError(t, err)
	assert.Zero(t, units)
	assert.Equal(t, before, scenario.driver.Calls())
}

func TestWorkflowCompactionPrefixHonorsClientCounter(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 3)
	model, err := scenario.fixture.runtime.models.ResolveModel(
		runtimeTestModelReference)
	require.NoError(t, err)
	model.Client = elelem.New(scenario.driver,
		elelem.WithClientTokenCounter(compactionTokenCounter{perMessage: 50}))
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: model,
	})
	require.NoError(t, err)
	before := scenario.driver.Calls()
	units, _, err := selectWorkflowCompactionPrefix(registry, input)
	require.NoError(t, err)
	assert.Zero(t, units)
	assert.Equal(t, before, scenario.driver.Calls())
}

func TestWorkflowCompactionPrefixRejectsNoEligibleHistory(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 1)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.MaxContextTokens = 200
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded
	before := scenario.driver.Calls()
	_, _, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
	require.ErrorIs(t, err, ErrCompactionUnavailable)
	assert.Equal(t, before, scenario.driver.Calls())
}

// The candidate request keeps the reply's reserve, so a summary allowance
// that only fits without it leaves the probe no budget to measure against.
func TestWorkflowCompactionPrefixRejectsAllowanceBesideReserve(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 3)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.OutputReserveTokens = conversation.MaxContextTokens -
		conversation.Compaction.MaxOutputTokens
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded
	before := scenario.driver.Calls()
	_, _, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
	require.ErrorIs(t, err, ErrCompactionUnavailable)
	assert.Equal(t, before, scenario.driver.Calls())
}

// The saved reserve comes off the context limit in the request a round sends.
func TestWorkflowModelRequestKeepsTheReplyReserve(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 2)
	models := scenario.fixture.runtime.models

	request, _, err := workflowModelRequest(models, input)
	require.NoError(t, err)
	reached, err := request.IsTokenLimitReached()
	require.NoError(t, err)
	require.False(t, reached, "the fixture fits without a reserve")

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.OutputReserveTokens = conversation.MaxContextTokens - 1
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded

	request, _, err = workflowModelRequest(models, input)
	require.NoError(t, err)
	reached, err = request.IsTokenLimitReached()
	require.NoError(t, err)
	assert.True(t, reached)
}

// A saved level outside the model's range is fitted before the request goes
// out. Sending it as asked made Elelem refuse the request and failed the turn.
func TestWorkflowModelRequestFitsTheReasoningEffort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		requested elelem.ReasoningEffort
		want      elelem.ReasoningEffort
	}{
		{
			name:      "below the model minimum",
			requested: elelem.ReasoningEffortMinimal,
			want:      elelem.ReasoningEffortLow,
		},
		{
			name:      "inside the range",
			requested: elelem.ReasoningEffortHigh,
			want:      elelem.ReasoningEffortHigh,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scenario, input := workflowCompactionPrefixFixture(t, 1)

			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(input.Execution.Context,
				&conversation))
			conversation.ReasoningEffort = tc.requested
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			input.Execution.Context = encoded

			model, err := scenario.fixture.runtime.models.ResolveModel(
				runtimeTestModelReference)
			require.NoError(t, err)
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
			model.Client = elelem.New(driver)
			model.Model.ReasoningLevels = elelem.ReasoningLevels{
				Min: elelem.ReasoningEffortLow,
			}
			registry, err := NewStaticRegistry(map[string]ModelClient{
				runtimeTestModelReference: model,
			})
			require.NoError(t, err)

			request, _, err := workflowModelRequest(registry, input)
			require.NoError(t, err)
			_, err = request.Run(t.Context())
			require.NoError(t, err)

			requests := driver.Requests()
			require.Len(t, requests, 1)
			assert.Equal(t, tc.want, requests[0].Params.ReasoningEffort)
		})
	}
}

func TestWorkflowCompactionPrefixRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 2)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.Compaction.Version++
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded
	before := scenario.driver.Calls()
	_, _, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Equal(t, before, scenario.driver.Calls())
}
