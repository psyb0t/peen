package mcpserver

const (
	serverName                  = "peen"
	serverVersion               = "1"
	objectOutputSchema          = `{"type":"object"}`
	defaultMaxOutputSchemaBytes = 65536
	uuidStringLength            = 36
)

const (
	toolSendMessage  = "sendMessage"
	toolListSessions = "listSessions"
	toolGetSession   = "getSession"
	toolReadMessages = "readMessages"
	toolCancelTurn   = "cancelTurn"
	toolListModels   = "listModels"
	toolListFiles    = "listWorkspaceFiles"
	toolReadFile     = "readWorkspaceFile"
	toolWriteFile    = "writeWorkspaceFile"
	toolDeleteFile   = "deleteWorkspaceFile"
)

const (
	failureInvalid   = "Invalid request. Check the tool parameters."
	failureDenied    = "The session or workspace is not allowed."
	failureMissing   = "The requested resource was not found."
	failureBusy      = "The session already has a running turn."
	failureCancelled = "The request was cancelled."
	failureTimeout   = "The turn timed out."
	failureInternal  = "The operation could not be completed."
)
