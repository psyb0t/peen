package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootBootstrapRetainsOriginalRequestAndOrigin(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	prepared.input.RequestID = uuid.Nil
	prepared.input.SystemPrompt = "Caller prompt."
	prepared.input.SystemPromptMode = PromptModeAppend
	prepared.input.ReasoningEffort = elelem.ReasoningEffortLow
	prepared.origin = &TurnOrigin{EventID: uuid.New(), EventType: "example.event"}
	prepared.input.SourceEventID = prepared.origin.EventID
	saved := fixture.base.runtime.rootWorkflowBootstrap(prepared)
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	var restored workflowRootBootstrap
	require.NoError(t, json.Unmarshal(encoded, &restored))
	input := restored.request(nil)
	assert.Equal(t, prepared.turn.requestID, input.RequestID)
	assert.Equal(t, prepared.input.SystemPrompt, input.SystemPrompt)
	assert.Equal(t, prepared.input.SystemPromptMode, input.SystemPromptMode)
	assert.Equal(t, prepared.input.ReasoningEffort, input.ReasoningEffort)
	assert.Equal(t, prepared.origin, input.Origin)
	assert.Equal(t, prepared.input.SourceEventID, input.SourceEventID)
	assert.Nil(t, input.OnEvent)
	input.Origin.EventType = "changed.event"
	assert.Equal(t, "example.event", restored.Origin.EventType)
}
