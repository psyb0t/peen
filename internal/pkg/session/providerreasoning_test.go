package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreKeepsProviderReasoningForLaterTurns(t *testing.T) {
	t.Parallel()

	const envelope = `{"reasoning_content":"earlier thought"}`

	fixture := newExecutionFixture(t)
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID,
		RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()})
	require.NoError(t, err)

	require.NoError(t, fixture.store.AppendCheckpoint(t.Context(), lease,
		[]MessageInput{
			{
				Role:                  models.MessageRoleAssistant,
				Content:               "with reasoning",
				ToolCallsJSON:         "[]",
				ProviderReasoningJSON: envelope,
			},
			{
				Role:          models.MessageRoleAssistant,
				Content:       "without reasoning",
				ToolCallsJSON: "[]",
			},
		}, nil))
	require.NoError(t, fixture.store.FinalizeTurn(t.Context(), lease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))

	history, err := fixture.store.CompletedHistory(t.Context(),
		fixture.sessionID)
	require.NoError(t, err)

	stored := map[string]string{}
	for _, message := range history.Messages {
		stored[message.Content] = message.ProviderReasoningJSON
	}

	assert.Equal(t, envelope, stored["with reasoning"])
	assert.Empty(t, stored["without reasoning"])
}
