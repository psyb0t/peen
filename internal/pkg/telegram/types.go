package telegram

import (
	"encoding/json"
	"net/http"
)

// Client talks only to Telegram's fixed Bot API host. Endpoints and credentials
// never appear in returned errors.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

//nolint:tagliatelle // Telegram defines these wire fields.
type Update struct {
	ID      int64    `json:"update_id"`
	Message *Message `json:"message,omitempty"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type Message struct {
	ID             int64       `json:"message_id"`
	Chat           Chat        `json:"chat"`
	From           *User       `json:"from,omitempty"`
	Text           string      `json:"text,omitempty"`
	Caption        string      `json:"caption,omitempty"`
	ReplyToMessage *Message    `json:"reply_to_message,omitempty"`
	Document       *Document   `json:"document,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	Voice          *Voice      `json:"voice,omitempty"`
}

type Chat struct {
	ID int64 `json:"id"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot,omitempty"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size,omitempty"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size,omitempty"`
	FilePath string `json:"file_path"`
}

type apiResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type updatesRequest struct {
	Offset         int64    `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type messageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

//nolint:tagliatelle // Telegram defines these wire fields.
type fileRequest struct {
	FileID string `json:"file_id"`
}
