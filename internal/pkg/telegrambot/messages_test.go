package telegrambot

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramChatSettingsAndFreshHistory(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	runUpdate(t, f, chatUpdate("/model example/model"))
	runUpdate(t, f, chatUpdate("/effort high"))
	runUpdate(t, f, chatUpdate("/system Be concise"))
	runUpdate(t, f, chatUpdate("Explain"))
	first := receivedRequest(t, f)
	assert.Equal(t, "Explain", first.Message.Message)
	require.NotNil(t, first.Message.Model)
	assert.Equal(t, "example/model", *first.Message.Model)
	require.NotNil(t, first.Message.ReasoningEffort)
	assert.Equal(t, "high", *first.Message.ReasoningEffort)
	require.NotNil(t, first.Message.SystemPrompt)
	assert.Equal(t, agent.PromptModeAppend, first.Message.SystemPrompt.Mode)
	assert.Equal(t, "Be concise", first.Message.SystemPrompt.Content)
	runUpdate(t, f, chatUpdate("/new"))
	runUpdate(t, f, chatUpdate("/system reset"))
	runUpdate(t, f, chatUpdate("Again"))
	second := receivedRequest(t, f)
	assert.NotEqual(t, first.SessionID, second.SessionID)
	assert.Nil(t, second.Message.SystemPrompt)
	runUpdate(t, f, chatUpdate("/model missing/model"))
	chat, err := f.store.Chat(t.Context(), -123)
	require.NoError(t, err)
	assert.Equal(t, "example/model", chat.Model)
}

func TestTelegramAllowlistsAndBusyCancellation(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	for _, update := range []telegram.Update{
		{},
		{Message: &telegram.Message{
			ID: 1, Chat: telegram.Chat{ID: 999},
			From: &telegram.User{ID: 7}, Text: "No",
		}},
		{Message: &telegram.Message{
			ID: 1, Chat: telegram.Chat{ID: -123},
			From: &telegram.User{ID: 99}, Text: "No",
		}},
	} {
		require.NoError(t, f.engine.Handle(t.Context(), update))
	}
	assert.Empty(t, f.seen)
	assert.Empty(t, f.client.messages)
	f.block = true
	require.NoError(t, f.engine.Handle(t.Context(), chatUpdate("Wait")))
	receivedRequest(t, f)
	require.NoError(t, f.engine.Handle(t.Context(), chatUpdate("Second")))
	require.NoError(t, f.engine.Handle(t.Context(), chatUpdate("/new")))
	require.NoError(t, f.engine.Handle(t.Context(), chatUpdate("/status")))
	require.NoError(t, f.engine.Handle(t.Context(), chatUpdate("/cancel")))
	waitBotIdle(t, f.engine)
	assert.Empty(t, f.seen)
	require.Len(t, f.client.messages, 4)
	assert.Equal(t, replyCancelled, f.client.messages[3].Text)
}

func TestTelegramUploadsAndFetchUseWorkspaceConfinement(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	f.client.downloads["document"] = []byte("saved bytes")
	update := chatUpdate("")
	update.Message.Caption = "Review"
	update.Message.Document = &telegram.Document{
		FileID: "document", FileName: "../../bad\\name.txt", FileSize: 11,
	}
	runUpdate(t, f, update)
	input := receivedRequest(t, f)
	assert.Contains(t, input.Message.Message, "Review")
	_, relative, found := strings.Cut(input.Message.Message, "Saved file: ")
	require.True(t, found)
	assert.True(t, strings.HasPrefix(relative, "uploads/"))
	assert.NotContains(t, relative, "..")
	content, err := f.registry.ReadWorkspaceFile(t.Context(), input.SessionID, relative)
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("saved bytes")),
		content.ContentBase64)
	runUpdate(t, f, chatUpdate("/fetch "+relative))
	require.Len(t, f.client.documents, 1)
	assert.Equal(t, []byte("saved bytes"), f.client.documents[0])
	runUpdate(t, f, chatUpdate("/fetch ../escape"))
	assert.Len(t, f.client.documents, 1)
	require.NoError(t, os.Symlink(t.TempDir(),
		filepath.Join(f.workspace, "chat_-123", "escape")))
	runUpdate(t, f, chatUpdate("/fetch escape/file"))
	assert.Len(t, f.client.documents, 1)
}

func TestTelegramRefusesRedirectedChatDirectories(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(f.workspace, "chat_-123")))
	runUpdate(t, f, chatUpdate("No access"))
	assert.Empty(t, f.seen)
}

func TestTelegramSplitsUnicodeTextWithoutLosingLineBreaks(t *testing.T) {
	t.Parallel()
	//nolint:gosmopolitan // Exercise Unicode character chunking.
	text := strings.Repeat("a", 3000) + "\n" + strings.Repeat("界", 5000)
	chunks := splitText(text)
	require.Len(t, chunks, 3)
	assert.Equal(t, text, strings.Join(chunks, ""))
	assert.True(t, strings.HasSuffix(chunks[0], "\n"))
	for _, chunk := range chunks {
		assert.LessOrEqual(t, utf8.RuneCountInString(chunk), textLimit)
	}
}

func TestTelegramSkipsQueuedUpdatesAndClosesPolling(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	f.client.polls = make(chan int64, 2)
	f.client.queued = []telegram.Update{chatUpdate("Old")}
	done := make(chan error, 1)
	go func() { done <- f.engine.Run(t.Context()) }()
	for _, expected := range []int64{-1, 11} {
		select {
		case offset := <-f.client.polls:
			assert.Equal(t, expected, offset)
		case <-time.After(5 * time.Second):
			t.Fatal("Telegram did not poll")
		}
	}
	f.engine.Close()
	require.NoError(t, <-done)
	assert.Empty(t, f.seen)
	require.Error(t, f.engine.Run(t.Context()))
	require.Error(t, f.engine.Handle(t.Context(), chatUpdate("Closed")))
}

func TestTelegramSystemResetClearsTheAppendedPrompt(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	runUpdate(t, f, chatUpdate("/system Answer in French"))
	runUpdate(t, f, chatUpdate("Hello"))
	first := receivedRequest(t, f)
	require.NotNil(t, first.Message.SystemPrompt)
	assert.Equal(t, agent.PromptModeAppend, first.Message.SystemPrompt.Mode)
	assert.Equal(t, "Answer in French", first.Message.SystemPrompt.Content)
	runUpdate(t, f, chatUpdate("/system reset"))
	runUpdate(t, f, chatUpdate("Again"))
	second := receivedRequest(t, f)
	assert.Equal(t, first.SessionID, second.SessionID)
	assert.Nil(t, second.Message.SystemPrompt)
}
