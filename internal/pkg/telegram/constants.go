package telegram

import "time"

const (
	apiBaseURL = "https://api.telegram.org"
)

const (
	maxJSONBytes         = 2 * 1024 * 1024
	maxUploadBytes       = 20 * 1024 * 1024
	maxPhotoBytes        = 10 * 1024 * 1024
	maxDownloadBytes     = 20 * 1024 * 1024
	maxFilePathBytes     = 1024
	maxFileIDBytes       = 1024
	maxFilenameBytes     = 256
	maxTokenBytes        = 300
	maxTokenSecretBytes  = 256
	maxTextRunes         = 4096
	maxTextBytes         = maxTextRunes * 4
	minimumUpdatesOffset = -1
	maxPollSeconds       = 50
	regularTimeout       = 30 * time.Second
	pollSlack            = 15 * time.Second
	clientTimeout        = maxPollSeconds*time.Second + pollSlack
)

const (
	methodGetUpdates   = "getUpdates"
	methodSendMessage  = "sendMessage"
	methodSendDocument = "sendDocument"
	methodSendPhoto    = "sendPhoto"
	methodGetFile      = "getFile"
	fieldChatID        = "chat_id"
	fieldDocument      = "document"
	fieldPhoto         = "photo"
)
