package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountQueuedMessagesCountsAcceptedMessages(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)

	count, err := fixture.store.CountQueuedMessages(t.Context())
	require.NoError(t, err)
	assert.Zero(t, count)

	_, err = fixture.store.AcceptExecutionMessage(
		t.Context(),
		fixture.root.ID,
		inboxTestLease(t, fixture),
		inboxTestInput(),
	)
	require.NoError(t, err)

	count, err = fixture.store.CountQueuedMessages(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
