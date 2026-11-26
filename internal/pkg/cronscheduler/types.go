// Package cronscheduler dispatches startup-loaded jobs through session workers.
package cronscheduler

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type Runner interface {
	Run(context.Context, control.ConversationRequest) (
		*control.ConversationResult, error,
	)
}

type Sessions interface {
	Open(context.Context, string, string) (control.OpenResult, error)
	ResolveWorkspace(string) (string, error)
	WorkspaceAgentSpace(context.Context, string) (string, bool, error)
	ValidateWorkspaceSession(context.Context, uuid.UUID) error
}

type Turns interface {
	GetTurnByRequest(context.Context, uuid.UUID, uuid.UUID) (
		*models.Turn, error,
	)
	GetTurnModelSummary(context.Context, uuid.UUID, uuid.UUID) (
		session.TurnModelSummary, error,
	)
}

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type Options struct {
	Jobs            []cronconfig.Job
	Store           *cronstore.Store
	Runner          Runner
	Sessions        Sessions
	Turns           Turns
	Clock           Clock
	MaxMessageBytes int
	Notifier        control.CronNotifier
}

type jobState struct {
	definition cronconfig.Job
	record     models.CronJob
}

// Scheduler owns its dispatch goroutines until Close returns. Close prevents
// a service launched during shutdown from starting another loop.
type Scheduler struct {
	options Options
	mutex   sync.Mutex
	jobs    []jobState
	active  map[string]int
	cancel  context.CancelFunc
	closed  bool
	started bool
	workers sync.WaitGroup
}
