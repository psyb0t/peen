package telegrambot

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramCronRepliesUseSavedChatMessagePair(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	opened, err := f.registry.Open(t.Context(), f.workspace, "control")
	require.NoError(t, err)
	cron, err := cronstore.New(f.handle)
	require.NoError(t, err)
	now := time.Now().UTC()
	run := &models.CronRun{
		ID: uuid.New(), JobName: "report", ScheduledAt: now,
		StartedAt: &now, SessionID: &opened.Session.ID,
		State: models.CronRunStateRunning,
	}
	claimed, err := cron.ClaimRun(t.Context(), run)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, f.engine.NotifyCron(t.Context(), control.CronNotification{
		RunID: run.ID, ChatID: -123, SessionID: opened.Session.ID, Text: "Report",
	}))
	require.Len(t, f.client.messages, 1)
	notificationID := f.client.messages[0].ID
	update := chatUpdate("Continue")
	update.Message.ReplyToMessage = &telegram.Message{
		ID: notificationID, Chat: telegram.Chat{ID: 999},
	}
	runUpdate(t, f, update)
	reply := receivedRequest(t, f)
	assert.Equal(t, opened.Session.ID, reply.SessionID)
	chat, err := f.store.Chat(t.Context(), -123)
	require.NoError(t, err)
	assert.NotEqual(t, opened.Session.ID, chat.SessionID)
	runUpdate(t, f, chatUpdate("Ordinary"))
	assert.Equal(t, chat.SessionID, receivedRequest(t, f).SessionID)
	update.Message.Chat.ID = -456
	runUpdate(t, f, update)
	assert.NotEqual(t, opened.Session.ID, receivedRequest(t, f).SessionID)
	require.ErrorIs(t, f.store.BindNotification(t.Context(), -123,
		notificationID, uuid.New()), commerr.ErrAlreadyExists)
	require.Error(t, f.engine.NotifyCron(t.Context(), control.CronNotification{
		RunID: run.ID, ChatID: 999, SessionID: opened.Session.ID, Text: "No",
	}))
}

func TestTelegramBindingAndChatStoreValidateKeys(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	require.Error(t, f.store.SaveChat(t.Context(), nil))
	require.Error(t, f.store.SaveChat(t.Context(), &models.TelegramChat{}))
	_, err := f.store.Chat(t.Context(), 0)
	require.Error(t, err)
	require.Error(t, f.store.BindNotification(t.Context(), 0, 1, uuid.New()))
	require.Error(t, f.store.BindNotification(t.Context(), -123, 0, uuid.New()))
	require.Error(t, f.store.BindNotification(t.Context(), -123, 1, uuid.Nil))
	_, err = f.store.ReplySession(t.Context(), 0, 1)
	require.Error(t, err)
	_, err = f.store.ReplySession(t.Context(), -123, 1)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	require.Error(t, f.store.BindNotification(t.Context(), -123, 1, uuid.New()))
}
