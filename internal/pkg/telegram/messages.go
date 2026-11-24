package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/psyb0t/aichteeteapee"
)

func (c *Client) GetUpdates(
	ctx context.Context, offset int64, timeoutSeconds int,
) ([]Update, error) {
	if offset < minimumUpdatesOffset || timeoutSeconds < 0 ||
		timeoutSeconds > maxPollSeconds {
		return nil, invalidInput()
	}

	payload, err := json.Marshal(updatesRequest{
		Offset:  offset,
		Timeout: timeoutSeconds, AllowedUpdates: []string{"message"},
	})
	if err != nil {
		return nil, invalidInput()
	}

	timeout := regularTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds)*time.Second + pollSlack
	}

	result, err := c.call(ctx, methodGetUpdates, bytes.NewReader(payload),
		aichteeteapee.ContentTypeJSON, timeout)
	if err != nil {
		return nil, err
	}

	var updates []Update

	if err := json.Unmarshal(result, &updates); err != nil {
		return nil, invalidResponse()
	}

	if err := validateUpdates(updates); err != nil {
		return nil, err
	}

	return updates, nil
}

func (c *Client) SendMessage(
	ctx context.Context, chatID int64, text string,
) (Message, error) {
	if chatID == 0 || len(text) > maxTextBytes || !utf8.ValidString(text) ||
		utf8.RuneCountInString(text) > maxTextRunes ||
		strings.TrimSpace(text) == "" {
		return Message{}, invalidInput()
	}

	payload, err := json.Marshal(messageRequest{ChatID: chatID, Text: text})
	if err != nil {
		return Message{}, invalidInput()
	}

	result, err := c.call(ctx, methodSendMessage, bytes.NewReader(payload),
		aichteeteapee.ContentTypeJSON, regularTimeout)
	if err != nil {
		return Message{}, err
	}

	return decodeMessage(result, chatID)
}

func (c *Client) SendDocument(
	ctx context.Context, chatID int64, name string, data []byte,
) (Message, error) {
	return c.sendUpload(ctx, chatID, name, data, fieldDocument)
}

func (c *Client) SendPhoto(
	ctx context.Context, chatID int64, name string, data []byte,
) (Message, error) {
	if len(data) == 0 {
		return Message{}, invalidInput()
	}

	if len(data) > maxPhotoBytes {
		return Message{}, sizeFailure()
	}

	return c.sendUpload(ctx, chatID, name, data, fieldPhoto)
}

func (c *Client) sendUpload(
	ctx context.Context, chatID int64, name string, data []byte, field string,
) (Message, error) {
	if chatID == 0 || !validFilename(name) {
		return Message{}, invalidInput()
	}

	if len(data) > maxUploadBytes {
		return Message{}, sizeFailure()
	}

	body, contentType, err := uploadBody(chatID, name, data, field)
	if err != nil {
		return Message{}, err
	}

	method := methodSendDocument
	if field == fieldPhoto {
		method = methodSendPhoto
	}

	result, err := c.call(ctx, method, body, contentType, regularTimeout)
	if err != nil {
		return Message{}, err
	}

	return decodeMessage(result, chatID)
}

func uploadBody(
	chatID int64, name string, data []byte, field string,
) (*bytes.Buffer, string, error) {
	body := new(bytes.Buffer)

	writer := multipart.NewWriter(body)
	if err := writer.WriteField(fieldChatID,
		strconv.FormatInt(chatID, 10)); err != nil {
		return nil, "", invalidInput()
	}

	part, err := writer.CreateFormFile(field, name)
	if err != nil {
		return nil, "", invalidInput()
	}

	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		return nil, "", invalidInput()
	}

	if err := writer.Close(); err != nil {
		return nil, "", invalidInput()
	}

	return body, writer.FormDataContentType(), nil
}

func decodeMessage(data []byte, chatID int64) (Message, error) {
	var message Message

	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, invalidResponse()
	}

	if message.ID <= 0 || message.Chat.ID != chatID {
		return Message{}, invalidResponse()
	}

	return message, nil
}

func validateUpdates(updates []Update) error {
	if updates == nil {
		return invalidResponse()
	}

	for _, update := range updates {
		if !validUpdate(update) {
			return invalidResponse()
		}
	}

	return nil
}

func validUpdate(update Update) bool {
	return update.ID > 0 && update.ID < math.MaxInt64 &&
		(update.Message == nil ||
			(update.Message.ID > 0 && update.Message.Chat.ID != 0))
}
