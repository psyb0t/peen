package telegrambot

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramStoredSystemSessionIsRefused(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	chat, err := f.engine.chat(t.Context(), -123, false)
	require.NoError(t, err)
	stored, err := f.turns.Get(t.Context(), chat.SessionID)
	require.NoError(t, err)
	system, err := f.turns.OpenWorkspace(t.Context(), stored.Workspace,
		session.OpenSessionOptions{
			RootAgent: harness.SystemAgentName,
			ModelID:   "example/model", AgentSpace: "control",
		})
	require.NoError(t, err)
	chat.SessionID = system.Session.ID
	require.NoError(t, f.store.SaveChat(t.Context(), chat))
	runUpdate(t, f, chatUpdate("Forbidden"))
	assert.Empty(t, f.seen)
	require.Error(t, f.engine.NotifyCron(t.Context(), control.CronNotification{
		SessionID: system.Session.ID, ChatID: -123, RunID: system.Session.ID,
		Text: "Forbidden",
	}))
}
