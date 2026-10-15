package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildPhaseBufferKeepsRecordsPrivateUntilCompletion(t *testing.T) {
	t.Parallel()
	fixture, dispatch, input, runID := childPhaseProjectionFixture(t, nil)
	shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
	require.NoError(t, err)
	before, err := fixture.base.store.ListAgentRunMessages(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	sink, err := newChildPhaseSink(t.Context(), shared.actor.sink, uuid.New(), 0)
	require.NoError(t, err)
	require.NoError(t, sink.onToolCallStart(t.Context(),
		workflowPhaseCallEvent(input.Tool)))
	require.NoError(t, sink.onMessageInjection(t.Context(), elelem.MessageInjection{
		Type: elelem.RoleUser, Content: "retained note",
	}))
	assert.Len(t, sink.phaseBuffer.Transcript.Messages, 1)
	require.Len(t, sink.phaseBuffer.Transcript.Events, 2)
	assert.NotEqual(t, uuid.Nil, sink.phaseBuffer.Transcript.Events[0].ID)
	after, err := fixture.base.store.ListAgentRunMessages(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
