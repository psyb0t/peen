package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inboxProjectionTestTimeout = 5 * time.Second

type inboxProjectionBarrierStore struct {
	session.Storage
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *inboxProjectionBarrierStore) AppendCheckpoint(
	ctx context.Context,
	lease session.Lease,
	messages []session.MessageInput,
	events []session.EventInput,
) error {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	if err := s.Storage.AppendCheckpoint(ctx, lease, messages, events); err != nil {
		return ctxerrors.Wrap(err, "write projection barrier checkpoint")
	}

	return nil
}

func TestStoredInboxProjectionPreservesConcurrentUnsavedTail(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	stored := session.MessageInput{
		Role: models.MessageRoleUser, Content: "already stored receipt",
		Workspace: prepared.workspace,
	}
	require.NoError(t, fixture.base.store.AppendCheckpoint(t.Context(),
		prepared.lease, []session.MessageInput{stored}, nil))
	prepared.turn.appendMessage(session.MessageInput{
		Role: models.MessageRoleAssistant, Content: "pending before receipt",
	})
	barrier := &inboxProjectionBarrierStore{
		Storage: fixture.base.store, entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(barrier.release) })
	t.Cleanup(release)
	prepared.turn.store = barrier
	finished := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				finished <- ctxerrors.New("inbox projection panicked")
			}
		}()
		finished <- prepared.turn.retainStoredInbox(t.Context(),
			[]session.MessageInput{stored}, nil)
	}()
	select {
	case <-barrier.entered:
	case <-time.After(inboxProjectionTestTimeout):
		t.Fatal("inbox projection did not reach the checkpoint")
	}
	prepared.turn.appendMessage(session.MessageInput{
		Role: models.MessageRoleAssistant, Content: "concurrent pending tail",
	})
	release()
	require.NoError(t, <-finished)
	pending := prepared.turn.pendingTranscript()
	require.Len(t, pending.messages, 1)
	assert.Equal(t, "concurrent pending tail", pending.messages[0].Content)
	require.NoError(t, prepared.turn.checkpoint(t.Context()))
	page, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		prepared.input.Message, stored.Content,
		"pending before receipt", "concurrent pending tail",
	},
		messageContents(page.Items))
}

func TestInboxReceiptEventsPreservesOriginalIdentity(t *testing.T) {
	t.Parallel()
	requestID, sourceID := uuid.New(), uuid.New()
	payload, err := json.Marshal(userMessagePayload{
		Message: "accepted text", SourceEventID: sourceID.String(),
	})
	require.NoError(t, err)
	events, err := inboxReceiptEvents([]session.EventInput{{
		RequestID: requestID, EventType: EventTypeUserMessageDelivered,
		PayloadJSON: string(payload),
	}})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, requestID, events[0].RequestID)
	assert.Equal(t, sourceID, events[0].TriggeringEventID)
	assert.JSONEq(t, string(payload), string(events[0].Payload))
}

func TestInboxReceiptEventsRefusesMalformedOrigin(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		payload string
	}{
		{name: "json", payload: `{`},
		{name: "identity", payload: `{"sourceEventId":"invalid"}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events, err := inboxReceiptEvents([]session.EventInput{{
				PayloadJSON: tc.payload,
			}})
			require.Error(t, err)
			assert.Nil(t, events)
		})
	}
}

func TestStoredInboxProjectionSinkFailureKeepsStoredData(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := session.EventInput{
		RequestID: uuid.New(), EventType: EventTypeUserMessageDelivered,
		PayloadJSON: `{"message":"retained message"}`,
	}
	require.NoError(t, fixture.base.store.AppendCheckpoint(t.Context(),
		prepared.lease, nil, []session.EventInput{input}))
	prepared.turn.sink = func(Event) error {
		return ctxerrors.Wrap(context.Canceled, "closed caller sink")
	}
	err := prepared.turn.projectStoredInbox(t.Context(),
		&session.ExecutionInboxDelivery{Events: []session.EventInput{input}})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, prepared.turn.checkpoint(t.Context()))
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeUserMessageDelivered},
		})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}
