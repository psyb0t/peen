package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitialRootConversationCanRestoreBeforeFirstActivity(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &saved))
	require.NotNil(t, saved.Root)
	require.NotNil(t, saved.Runtime)
	assert.Equal(t, prepared.turn.requestID, saved.Root.RequestID)
	assert.Equal(t, prepared.prompt.SystemMessage(), saved.SystemPrompt)
	fixture.base.store.ReleaseTurn(prepared.lease)
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(t.Context(),
		fixture.input, session.RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(),
		}, nil)
	require.NoError(t, err)
	assert.Equal(t, prepared.lease.TurnID, restored.lease.TurnID)
	activities, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Empty(t, activities.Items)
}

func TestRootConversationRetainsCompactionState(t *testing.T) {
	t.Parallel()
	scenario := setupCompactionScenario(t, 2)
	runtime := scenario.fixture.runtime
	prepared, err := runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: scenario.fixture.workspace,
		Message: "continue example work",
	})
	require.NoError(t, err)
	t.Cleanup(func() { scenario.fixture.store.ReleaseTurn(prepared.lease) })
	scope, err := runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	conversation, err := runtime.rootWorkflowConversation(prepared, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	var saved struct {
		Compaction json.RawMessage `json:"compaction"`
	}
	require.NoError(t, json.Unmarshal(encoded, &saved))
	require.NotEmpty(t, saved.Compaction)
}
