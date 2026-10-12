package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreChildInboxDoesNotRepeatCheckpointedMessages(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		checkpointed    int
		acknowledged    int
		undeliveredLast bool
	}{
		{name: "accepted only"},
		{name: "checkpoint before delivery event", checkpointed: 1},
		{name: "identical messages with one delivery", checkpointed: 1, acknowledged: 1},
		{name: "both checkpointed", checkpointed: 2},
		{name: "both acknowledged", checkpointed: 2, acknowledged: 2},
		{name: "identical unread message after missing delivery event", checkpointed: 1, undeliveredLast: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, run, driver := recoveryFixture(t)
			ctx := context.Background()
			_, err := fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{{Role: models.MessageRoleUser, Content: "seed task"}})
			require.NoError(t, err)
			ids := []uuid.UUID{uuid.New(), uuid.New()}
			for index := range 2 {
				payload := agentInboxMessagePayload{Message: "accepted text"}
				if tc.undeliveredLast {
					payload.AcceptanceID = &ids[index]
				}
				encoded, err := json.Marshal(payload)
				require.NoError(t, err)
				_, err = fixture.store.AppendAgentRunEvent(ctx, run.SessionID, run.ID, session.AgentRunEventInput{EventType: EventTypeAgentRunMessageQueued, PayloadJSON: string(encoded)})
				require.NoError(t, err)
			}
			for range tc.checkpointed {
				_, err := fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{{Role: models.MessageRoleUser, Content: "accepted text"}})
				require.NoError(t, err)
			}
			for range tc.acknowledged {
				_, err := fixture.store.AppendAgentRunEvent(ctx, run.SessionID, run.ID, session.AgentRunEventInput{EventType: EventTypeAgentRunMessageDelivered, PayloadJSON: `{"message":"accepted text"}`})
				require.NoError(t, err)
			}
			want := 2
			if tc.undeliveredLast {
				payload, err := json.Marshal(agentInboxMessagePayload{Message: "accepted text", AcceptanceID: &ids[1]})
				require.NoError(t, err)
				_, err = fixture.store.AppendAgentRunEvent(ctx, run.SessionID, run.ID, session.AgentRunEventInput{EventType: EventTypeAgentRunMessageUndelivered, PayloadJSON: string(payload)})
				require.NoError(t, err)
				want = 1
			}
			_ = restartRecoveryFixture(t, fixture)
			awaitRecoveredChild(t, fixture, run)
			requests := driver.Requests()
			require.Len(t, requests, 1)
			count := 0
			for _, message := range requests[0].Messages {
				if message.Text() == "accepted text" {
					count++
				}
			}
			assert.Equal(t, want, count)
			history, err := fixture.store.AgentRunHistory(ctx, run.SessionID, run.ID)
			require.NoError(t, err)
			count = 0
			for _, message := range history.Messages {
				if message.Content == "accepted text" {
					count++
				}
			}
			assert.Equal(t, want, count)
		})
	}
}
