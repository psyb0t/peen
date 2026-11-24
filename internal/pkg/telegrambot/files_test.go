package telegrambot

import (
	"bytes"
	"image"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramPhotoAndVoiceUploads(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	f.client.downloads["photo"] = encoded.Bytes()
	f.client.downloads["voice"] = []byte("voice bytes")
	update := chatUpdate("")
	update.Message.Photo = []telegram.PhotoSize{{FileID: "photo"}}
	update.Message.Voice = &telegram.Voice{FileID: "voice"}
	runUpdate(t, f, update)
	input := receivedRequest(t, f)
	require.Len(t, input.Message.Images, 1)
	assert.Equal(t, "image/png", input.Message.Images[0].MediaType)
	assert.Equal(t, encoded.Bytes(), input.Message.Images[0].Data)
	assert.Contains(t, input.Message.Message, "voice.ogg")
	assert.Contains(t, input.Message.Message, "photo.jpg")
}

func TestTelegramUploadBoundsAndInvalidImages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		declared int64
		data     []byte
	}{
		{"declared too large", maximumFileBytes + 1, nil},
		{"actual too large", 0, []byte("oversized")},
		{"invalid image", 0, []byte("bad")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newBotFixture(t)
			f.engine.options.MaxImageBytes = 3
			f.client.downloads["photo"] = tc.data
			update := chatUpdate("")
			update.Message.Photo = []telegram.PhotoSize{
				{FileID: "photo", FileSize: tc.declared},
			}
			runUpdate(t, f, update)
			assert.Empty(t, f.seen)
		})
	}
}

func TestTelegramChatDirectoriesKeepTheChatIDSign(t *testing.T) {
	t.Parallel()
	f := newBotFixture(t)
	group, err := f.engine.chatWorkspace(-123)
	require.NoError(t, err)
	user, err := f.engine.chatWorkspace(123)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.workspace, "chat_-123"), group)
	assert.Equal(t, filepath.Join(f.workspace, "chat_123"), user)
	assert.DirExists(t, group)
	assert.DirExists(t, user)
}
