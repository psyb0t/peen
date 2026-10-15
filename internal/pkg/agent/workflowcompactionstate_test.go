package agent

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workflowCompactionStateFixture(
	t *testing.T,
) (*compactor, []elelem.Message) {
	t.Helper()

	return &compactor{
			options: compactionOptions{
				ModelReference: "example/model", Prompt: "summarize prior work",
				PromptHash: "example-hash", MaxOutputTokens: 50,
				Timeout: time.Minute,
			},
			plan: reconstructionPlan{LeadCount: 1, Units: []reconstructionUnit{{
				FromMessageID: uuid.New(), ToMessageID: uuid.New(), TurnID: uuid.New(),
				FromSequence: 1, ToSequence: 2, MessageCount: 2,
				Roles: []elelem.Role{elelem.RoleUser, elelem.RoleAssistant},
			}}},
		}, []elelem.Message{
			{Role: elelem.RoleSystem, Content: elelem.Text("instructions")},
			{Role: elelem.RoleUser, Content: elelem.Text("completed request")},
			{Role: elelem.RoleAssistant, Content: elelem.Text("completed answer")},
		}
}

func TestWorkflowCompactionStateFreezesAndRoundTrips(t *testing.T) {
	t.Parallel()
	source, messages := workflowCompactionStateFixture(t)
	state := snapshotWorkflowCompaction(source)
	require.NoError(t, state.validate(messages))
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	var restored workflowCompactionState
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, *state, restored)
	require.NoError(t, restored.validate(messages))
	source.plan.Units[0].Roles[0] = elelem.RoleTool
	source.plan.Units[0].FromSequence = 42
	source.options.Prompt = "changed instructions"
	assert.Equal(t, elelem.RoleUser, state.Plan.Units[0].Roles[0])
	assert.EqualValues(t, 1, state.Plan.Units[0].FromSequence)
	assert.Equal(t, "summarize prior work", state.Prompt)
}

func TestWorkflowCompactionStateFreezesHead(t *testing.T) {
	t.Parallel()
	source, messages := workflowCompactionStateFixture(t)
	source.active = &compactionHead{
		ID: uuid.New(), FromMessageID: uuid.New(),
		FromSequence: 1, SourceMessageCount: 2,
	}
	source.plan.HasSummary = true
	source.plan.LeadCount = 2
	source.plan.Units[0].FromSequence = 3
	source.plan.Units[0].ToSequence = 4
	messages = append(messages[:1], append([]elelem.Message{{
		Role: elelem.RoleUser, Content: elelem.Text("prior summary"),
	}}, messages[1:]...)...)
	state := snapshotWorkflowCompaction(source)
	require.NoError(t, state.validate(messages))
	source.active.SourceMessageCount = 9
	assert.EqualValues(t, 2, state.Head.SourceMessageCount)
}

func TestWorkflowCompactionStateRejectsMalformedData(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowCompactionState)
		wantErr error
	}{
		{
			name: "version", wantErr: commerr.ErrValidationFailed,
			change: func(state *workflowCompactionState) { state.Version++ },
		},
		{
			name: "model", wantErr: ErrInvalidCompactionOptions,
			change: func(state *workflowCompactionState) { state.ModelReference = "" },
		},
		{
			name: "prompt", wantErr: ErrInvalidCompactionOptions,
			change: func(state *workflowCompactionState) { state.Prompt = " " },
		},
		{
			name: "prompt hash", wantErr: ErrInvalidCompactionOptions,
			change: func(state *workflowCompactionState) { state.PromptHash = "" },
		},
		{
			name: "output budget", wantErr: ErrInvalidCompactionOptions,
			change: func(state *workflowCompactionState) { state.MaxOutputTokens = 0 },
		},
		{
			name: "timeout", wantErr: ErrInvalidCompactionOptions,
			change: func(state *workflowCompactionState) { state.Timeout = 0 },
		},
		{
			name: "negative index", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.LeadCount = -1 },
		},
		{
			name: "overflow index", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) {
				state.Plan.LeadCount = math.MaxInt
			},
		},
		{
			name: "missing head", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.HasSummary = true },
		},
		{
			name: "unexpected head", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Head = &compactionHead{} },
		},
		{
			name: "invalid predecessor", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) {
				state.Plan.HasSummary = true
				state.Head = &compactionHead{}
			},
		},
		{
			name: "empty unit", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.Units[0].MessageCount = 0 },
		},
		{
			name: "overflow count", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) {
				state.Plan.Units[0].MessageCount = math.MaxInt
			},
		},
		{
			name: "missing role", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.Units[0].Roles = nil },
		},
		{
			name: "changed role", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) {
				state.Plan.Units[0].Roles[0] = elelem.RoleTool
			},
		},
		{
			name: "reversed range", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.Units[0].ToSequence = 0 },
		},
		{
			name: "missing source id", wantErr: ErrCompactionPlanMismatch,
			change: func(state *workflowCompactionState) { state.Plan.Units[0].FromMessageID = uuid.Nil },
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source, messages := workflowCompactionStateFixture(t)
			state := snapshotWorkflowCompaction(source)
			tc.change(state)
			require.ErrorIs(t, state.validate(messages), tc.wantErr)
		})
	}
}

func TestWorkflowCompactionStateAcceptsLegacyAbsence(t *testing.T) {
	t.Parallel()
	assert.Nil(t, snapshotWorkflowCompaction(nil))
	var state *workflowCompactionState
	require.NoError(t, state.validate(nil))
	require.NoError(t, validateWorkflowCompactionConversation(
		workflowConversation{}))
}
