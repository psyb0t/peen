package telegrambot

import "time"

const (
	maximumAttachments  = 3
	textLimit           = 4096
	pollSeconds         = 30
	retryDelay          = time.Second
	maximumFileBytes    = 20 << 20
	defaultMessageBytes = 262144
	defaultPromptBytes  = 65536
	defaultImageBytes   = 5 << 20
	defaultImages       = 4
	directoryMode       = 0o750
	uploadsDirectory    = "uploads"
	chatDirectoryPrefix = "chat_"
	maximumNameBytes    = 128
	replyBusy           = "Busy. Use /cancel to cancel this chat's turn."
	replyIdle           = "Idle."
	replyCancelled      = "Cancellation requested."
	replyFailed         = "The request failed."
	replyEmpty          = "The turn completed without text."
	replyNew            = "Started a fresh session."
	replyUnknownCommand = "Unknown command. Use /help."
	helpText            = `/new
/model [id]
/effort [level]
/system [text|reset]
/cancel
/status
/fetch <path>
/help`
	commandNew    = "/new"
	commandModel  = "/model"
	commandEffort = "/effort"
	commandSystem = "/system"
	commandCancel = "/cancel"
	commandStatus = "/status"
	commandFetch  = "/fetch"
	commandHelp   = "/help"
	resetArgument = "reset"
)
