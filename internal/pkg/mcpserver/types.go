package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// Runner routes turns through the controller's worker boundary.
type Runner interface {
	Run(context.Context, control.ConversationRequest) (
		*control.ConversationResult, error,
	)
}

// Sessions reads session pages and validates regular workspace access.
type Sessions interface {
	ListSessions(context.Context, api.ListSessionsParams) (
		api.SessionPage, error,
	)
	ValidateWorkspaceSession(context.Context, uuid.UUID) error
}

// Reader exposes stored reads and the configured model catalogue.
type Reader interface {
	Session(context.Context, uuid.UUID) (*api.Session, error)
	ListMessages(context.Context, api.ListMessagesParams) (
		*api.MessagePage, error,
	)
	ListModels() api.ModelList
}

// Turns owns cancellation of a selected regular session.
type Turns interface {
	CancelSessionTurn(context.Context, uuid.UUID) (bool, error)
}

// Dependencies uses the same controller services as the REST surface.
type Dependencies struct {
	Runner               Runner
	Sessions             Sessions
	Reader               Reader
	Turns                Turns
	Files                control.WorkspaceFileService
	MaxOutputSchemaBytes int
}

type tools struct {
	dependencies Dependencies
}

type schema = map[string]any

//nolint:lll // Immutable JSON Schema field descriptions.
type sendInput struct {
	Session   string `json:"sessionId,omitempty"       jsonschema:"Existing session ID from listSessions; omit for workspace."`
	Workspace string `json:"workspace,omitempty"       jsonschema:"Workspace path allowed by the operator; omit for sessionId."`
	Space     string `json:"agentSpace,omitempty"      jsonschema:"Operator-configured agent space; workspace selector only."`
	Message   string `json:"message"                   jsonschema:"Message to send."`
	Model     string `json:"model,omitempty"           jsonschema:"Configured ID from listModels; omitted keeps the default."`
	Effort    string `json:"reasoningEffort,omitempty" jsonschema:"Reasoning effort: minimal, low, medium, high, xhigh or max."`
	Schema    schema `json:"outputSchema,omitempty"    jsonschema:"Object JSON Schema for the final output; external refs refused."`
	Timeout   *int   `json:"timeoutSeconds,omitempty"  jsonschema:"Turn timeout in seconds, 1 through 86400; omit for default."`
}

type sendResult struct {
	SessionID   string          `json:"sessionId"`
	Text        string          `json:"text"`
	Output      json.RawMessage `json:"output,omitempty"`
	OutputError string          `json:"outputError,omitempty"`
	State       api.TurnState   `json:"state"`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type listSessionsInput struct {
	Limit  *int32 `json:"limit,omitempty"  jsonschema:"Page size, 1 to 200."`
	Offset *int32 `json:"offset,omitempty" jsonschema:"Nonnegative offset from the prior raw page offset plus limit."`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type sessionInput struct {
	Session string `json:"sessionId" jsonschema:"Regular session ID from listSessions or sendMessage."`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type readMessagesInput struct {
	Session string `json:"sessionId"       jsonschema:"Regular session ID from listSessions or sendMessage."`
	Limit   *int32 `json:"limit,omitempty" jsonschema:"Last messages, 1 to 200."`
}

type emptyInput struct{}

type cancelResult struct {
	Cancelled bool `json:"cancelled"`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type listFilesInput struct {
	Session string `json:"sessionId"      jsonschema:"Regular session ID from listSessions or sendMessage."`
	Path    string `json:"path,omitempty" jsonschema:"Relative workspace path; omit to list its root."`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type fileInput struct {
	Session string `json:"sessionId" jsonschema:"Regular session ID from listSessions or sendMessage."`
	Path    string `json:"path"      jsonschema:"Relative workspace path."`
}

//nolint:lll // Immutable JSON Schema field descriptions.
type writeFileInput struct {
	Session string `json:"sessionId"                   jsonschema:"Regular session ID from listSessions or sendMessage."`
	Path    string `json:"path"                        jsonschema:"Relative workspace path."`
	Content string `json:"contentBase64"               jsonschema:"Base64 file bytes."`
	Dirs    bool   `json:"createDirectories,omitempty" jsonschema:"Create missing parent directories before writing."`
}

type deleteResult struct {
	Deleted bool `json:"deleted"`
}
