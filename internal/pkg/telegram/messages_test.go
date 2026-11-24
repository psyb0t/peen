package telegram

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatesCarryOffsetAndMedia(t *testing.T) {
	t.Parallel()
	request := make(chan updatesRequest, 1)
	client := testClient(t, func(
		w http.ResponseWriter, r *http.Request,
	) {
		assert.Equal(t, "/bot"+testToken+"/getUpdates", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		var body updatesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		request <- body
		message := sentMessage(-12)
		message.From = &User{ID: 5, IsBot: true}
		message.Text, message.Caption = "hello", "caption"
		message.Document = &Document{FileID: "doc", FileName: "file.txt"}
		message.Photo = []PhotoSize{{FileID: "photo", Width: 2, Height: 2}}
		message.Voice = &Voice{FileID: "voice", Duration: 2}
		message.ReplyToMessage = &Message{ID: 6, Chat: message.Chat}
		writeResult(t, w, []Update{{ID: 9, Message: &message}})
	})
	updates, err := client.GetUpdates(t.Context(), 8, 30)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.EqualValues(t, 9, updates[0].ID)
	require.NotNil(t, updates[0].Message)
	assert.Equal(t, "hello", updates[0].Message.Text)
	assert.True(t, updates[0].Message.From.IsBot)
	assert.Equal(t, "doc", updates[0].Message.Document.FileID)
	assert.Equal(t, "photo", updates[0].Message.Photo[0].FileID)
	assert.Equal(t, "voice", updates[0].Message.Voice.FileID)
	assert.EqualValues(t, 6, updates[0].Message.ReplyToMessage.ID)
	body := <-request
	assert.EqualValues(t, 8, body.Offset)
	assert.Equal(t, 30, body.Timeout)
	assert.Equal(t, []string{"message"}, body.AllowedUpdates)
}

func TestSendMessage(t *testing.T) {
	t.Parallel()
	sent := make(chan messageRequest, 1)
	client := testClient(t, func(
		w http.ResponseWriter, r *http.Request,
	) {
		assert.Equal(t, "/bot"+testToken+"/sendMessage", r.URL.Path)
		var body messageRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sent <- body
		writeResult(t, w, sentMessage(body.ChatID))
	})
	message, err := client.SendMessage(t.Context(), -12, "hello")
	require.NoError(t, err)
	assert.EqualValues(t, 7, message.ID)
	assert.Equal(t, messageRequest{ChatID: -12, Text: "hello"}, <-sent)
}

func TestSendMultipart(t *testing.T) {
	t.Parallel()
	for _, field := range []string{fieldDocument, fieldPhoto} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			client := testClient(t, func(
				w http.ResponseWriter, r *http.Request,
			) {
				expected := methodSendDocument
				if field == fieldPhoto {
					expected = methodSendPhoto
				}
				assert.Equal(t, "/bot"+testToken+"/"+expected, r.URL.Path)
				name, data := readMultipartUpload(t, w, r, field, "-12")
				assert.Equal(t, "file.txt", name)
				assert.Equal(t, []byte("bytes"), data)
				writeResult(t, w, sentMessage(-12))
			})
			var message Message
			var err error
			if field == fieldDocument {
				message, err = client.SendDocument(t.Context(), -12,
					"file.txt", []byte("bytes"))
			} else {
				message, err = client.SendPhoto(t.Context(), -12,
					"file.txt", []byte("bytes"))
			}
			require.NoError(t, err)
			assert.EqualValues(t, -12, message.Chat.ID)
		})
	}
}

func TestStartupTailOffsetIsSerialized(t *testing.T) {
	t.Parallel()
	requests := make(chan updatesRequest, 1)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request updatesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests <- request
		writeResult(t, w, []Update{})
	})
	updates, err := client.GetUpdates(t.Context(), -1, 0)
	require.NoError(t, err)
	assert.Empty(t, updates)
	request := <-requests
	assert.EqualValues(t, -1, request.Offset)
	assert.Zero(t, request.Timeout)
}

func TestSendEmptyDocument(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		name, data := readMultipartUpload(t, w, r, fieldDocument, "12")
		assert.Empty(t, data)
		assert.Equal(t, "empty.txt", name)
		writeResult(t, w, sentMessage(12))
	})
	message, err := client.SendDocument(t.Context(), 12, "empty.txt", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 7, message.ID)
}
