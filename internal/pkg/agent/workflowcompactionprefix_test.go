package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
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
	units, err := selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
	require.NoError(t, err)
	assert.Equal(t, 4, units)
	assert.Equal(t, before, scenario.driver.Calls())
}

func TestWorkflowCompactionPrefixSkipsUnderBudget(t *testing.T) {
	t.Parallel()
	scenario, input := workflowCompactionPrefixFixture(t, 2)
	before := scenario.driver.Calls()
	units, err := selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
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
	units, err := selectWorkflowCompactionPrefix(registry, input)
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
	_, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
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
	_, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
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
	_, err = selectWorkflowCompactionPrefix(scenario.fixture.runtime.models,
		input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Equal(t, before, scenario.driver.Calls())
}
