package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootPhaseBufferPreservesPendingWritesWhileRetainingCommittedRecords(
	t *testing.T,
) {
	t.Parallel()
	turn := &runtimeTurn{
		messages:             []session.MessageInput{{Content: "committed"}, {Content: "pending"}},
		events:               []Event{{Type: "committed"}, {Type: "pending"}},
		checkpointedMessages: 1, checkpointedEvents: 1,
	}
	turn.retainRootPhaseProjection(session.ExecutionRootProjection{
		Messages: []session.MessageInput{{Content: "phase"}},
	}, []Event{{Type: "phase"}})
	assert.Equal(t, "phase", turn.messageSnapshot()[1].Content)
	pending := turn.pendingTranscript()
	require.Len(t, pending.messages, 1)
	assert.Equal(t, "pending", pending.messages[0].Content)
	require.Len(t, pending.events, 1)
	assert.Equal(t, "pending", pending.events[0].EventType)
}

func TestRootPhaseBufferRetainsIdentityWithoutSharingLiveSinks(t *testing.T) {
	t.Parallel()
	original := &runtimeTurn{
		requestID: uuid.New(), triggeringEventID: uuid.New(),
		workerGenerationID: "original", sink: func(Event) error { return nil },
	}
	buffer := newRootPhaseTurn(original)
	assert.Equal(t, original.requestID, buffer.requestID)
	assert.Equal(t, original.triggeringEventID, buffer.triggeringEventID)
	assert.Equal(t, original.workerGenerationID, buffer.workerGenerationID)
	assert.Nil(t, buffer.store)
	assert.Nil(t, buffer.sink)
	assert.Nil(t, buffer.observer)
}
