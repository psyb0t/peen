// Package telegrambot routes allowlisted Telegram messages to regular workers.
package telegrambot

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/psyb0t/peen/internal/pkg/telegramstore"
)

type Client interface {
	GetUpdates(context.Context, int64, int) ([]telegram.Update, error)
	SendMessage(context.Context, int64, string) (telegram.Message, error)
	SendDocument(context.Context, int64, string, []byte) (
		telegram.Message, error,
	)
	Download(context.Context, string, int64) ([]byte, error)
}

type Runner interface {
	Run(context.Context, control.ConversationRequest) (
		*control.ConversationResult, error,
	)
}

type Sessions interface {
	OpenFresh(context.Context, string, string) (control.OpenResult, error)
	ResolveWorkspace(string) (string, error)
	ValidateWorkspaceSession(context.Context, uuid.UUID) error
	ReadWorkspaceFile(context.Context, uuid.UUID, string) (
		control.WorkspaceFileContent, error,
	)
	WriteWorkspaceFile(context.Context, uuid.UUID, string, string, bool) (
		control.WorkspaceFileEntry, error,
	)
}

type SessionReader interface {
	Get(context.Context, uuid.UUID) (*models.Session, error)
}

type Options struct {
	Client               Client
	Runner               Runner
	Sessions             Sessions
	SessionReader        SessionReader
	Store                *telegramstore.Store
	Policy               config.TelegramPolicy
	Workspace            string
	AgentSpace           string
	Models               []string
	MaxMessageBytes      int
	MaxSystemPromptBytes int
	MaxFileBytes         int64
	MaxImageBytes        int
	MaxImages            int
}

type Engine struct {
	options Options
	mutex   sync.Mutex
	active  map[int64]context.CancelFunc
	workers sync.WaitGroup
	cancel  context.CancelFunc
	started bool
	closed  bool
}
