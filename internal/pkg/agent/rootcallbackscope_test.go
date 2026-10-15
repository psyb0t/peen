package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCallbackRefreshKeepsPendingQueueAndArrivalChannel(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	queue := shared.prepared.userMessages
	require.NoError(t, queue.enqueue(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "use :core:planning",
	}))
	_, arrived := queue.arrival()
	require.NoError(t, callbacks.refresh(t.Context(), shared, fixture.input))
	assert.Same(t, queue, shared.prepared.userMessages)
	count, after := queue.arrival()
	assert.Equal(t, arrived, after)
	assert.Positive(t, count)
	assert.Contains(t, queue.skills.held(), "core:planning")
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}
