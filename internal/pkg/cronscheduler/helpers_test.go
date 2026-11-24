package cronscheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

const (
	sandboxSpace = "docker:sandbox"
	sandboxImage = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
)

type fixtureClock struct {
	mutex sync.Mutex
	now   time.Time
	wake  chan time.Time
	armed chan struct{}
}

func (c *fixtureClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.now
}

func (c *fixtureClock) After(time.Duration) <-chan time.Time {
	select {
	case c.armed <- struct{}{}:
	default:
	}

	return c.wake
}

func (c *fixtureClock) advance(delay time.Duration) {
	c.mutex.Lock()
	c.now = c.now.Add(delay)
	c.mutex.Unlock()
}

type schedulerFixture struct {
	clock       *fixtureClock
	store       *cronstore.Store
	turns       *session.Store
	sessions    *control.Registry
	workspace   string
	seen        chan control.ConversationRequest
	release     <-chan struct{}
	err         error
	state       models.TurnState
	outputError string
}

func newSchedulerFixture(t *testing.T) *schedulerFixture {
	t.Helper()
	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	clock := &fixtureClock{
		now:  time.Unix(1000, 0).UTC(),
		wake: make(chan time.Time, 1), armed: make(chan struct{}, 1),
	}
	turns, err := session.NewStore(handle, session.Options{Clock: clock.Now})
	require.NoError(t, err)
	store, err := cronstore.New(handle)
	require.NoError(t, err)
	workspace := t.TempDir()
	policy, err := control.NewWorkspacePolicy([]string{workspace})
	require.NoError(t, err)
	spaces, err := worker.NewAgentSpaceSet([]worker.AgentSpace{
		{Name: "control", Kind: worker.KindControl},
		{
			Name: sandboxSpace, Kind: worker.KindDocker, Image: sandboxImage,
			Docker: worker.DockerAccessNone, Network: worker.NetworkModeBridge,
		},
	}, "control")
	require.NoError(t, err)
	sessions, err := control.NewRegistry(control.RegistryOptions{
		Store: turns, Policy: policy, AgentSpaces: spaces,
		RootAgent: "core:general-purpose", DefaultModel: "example/model",
	})
	require.NoError(t, err)

	return &schedulerFixture{
		clock: clock, store: store, turns: turns,
		sessions: sessions, workspace: workspace,
		seen:  make(chan control.ConversationRequest, 10),
		state: models.TurnStateCompleted,
	}
}

func (f *schedulerFixture) job() cronconfig.Job {
	return cronconfig.Job{
		Name: "nightlyReport", ScheduleText: "@every 1s",
		Schedule: cron.Every(time.Second), TimeZone: "UTC", Workspace: f.workspace,
		AgentSpace: "control", Session: cronconfig.SessionContinue,
		Message: "Run {jobName} at {scheduledAt}",
	}
}

func (f *schedulerFixture) options(job cronconfig.Job) Options {
	return Options{
		Jobs: []cronconfig.Job{job}, Store: f.store, Runner: f,
		Sessions: f.sessions, Turns: f.turns, Clock: f.clock,
	}
}

func (f *schedulerFixture) Run(ctx context.Context,
	input control.ConversationRequest,
) (*control.ConversationResult, error) {
	f.seen <- input
	if f.err != nil {
		return nil, f.err
	}
	lease, err := f.turns.AcquireTurn(ctx, input.SessionID,
		session.StartTurnInput{RequestID: input.RequestID, Workspace: f.workspace})
	if err != nil {
		return nil, err
	}
	defer f.turns.ReleaseTurn(lease)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := f.turns.FinalizeTurn(ctx, lease, session.FinalizeTurnInput{
		State: f.state, OutputError: f.outputError,
	}); err != nil {
		return nil, err
	}

	return &control.ConversationResult{
		SessionID: input.SessionID,
		RequestID: input.RequestID, TurnID: lease.TurnID,
		State: api.TurnState(f.state), OutputError: f.outputError,
	}, nil
}

func waitIdle(t *testing.T, s *Scheduler) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mutex.Lock()
		defer s.mutex.Unlock()
		for _, count := range s.active {
			if count != 0 {
				return false
			}
		}

		return true
	}, 5*time.Second, time.Millisecond)
}

func nextInput(t *testing.T, f *schedulerFixture) control.ConversationRequest {
	t.Helper()
	select {
	case input := <-f.seen:
		return input
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled message did not reach runner")

		return control.ConversationRequest{}
	}
}
