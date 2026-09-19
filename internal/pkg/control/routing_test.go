package control_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	routedAnswer      = "the worker answered"
	routedMessage     = "do the work"
	routedEventType   = "message.delta"
	socketRootMode    = 0o700
	routingReadyWait  = 5 * time.Second
	routingStopWait   = 2 * time.Second
	routedSignal      = "stop"
	routedSignalState = "signalled"

	routedTestImageTag = "psyb0t/peen@sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222"
)

// routedWorker is a worker that connects in process and answers the commands a
// controller sends.
//
// It stands in for the `peen worker` process so this test exercises the real
// protocol, the real supervisor, and the real router without spawning anything.
type routedWorker struct {
	connection *protocol.Connection
	document   worker.LaunchDocument

	turns     []protocol.RunTurn
	sessionID uuid.UUID
	workspace string

	// signals records every decoded signal_job payload. Decoding it here is the
	// point: a command whose payload is encoded twice still arrives and still
	// answers, so only a worker that reads the fields catches it.
	signals []protocol.SignalJob
}

// signalJob decodes the command the way the real worker does.
func (w *routedWorker) signalJob(
	payload json.RawMessage,
) (protocol.SignalJobResult, error) {
	request := protocol.SignalJob{}
	if err := json.Unmarshal(payload, &request); err != nil {
		return protocol.SignalJobResult{}, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"decode the signal_job command",
		)
	}

	w.signals = append(w.signals, request)

	return protocol.SignalJobResult{
		Handled:   true,
		Signalled: true,
		State:     routedSignalState,
	}, nil
}

func (w *routedWorker) HandleCall(
	context.Context,
	string,
	json.RawMessage,
) (any, error) {
	return nil, commerr.ErrNotImplemented
}

// HandleCommand answers a run_turn by doing what a real worker does: it writes
// the turn's records through the protocol, then reports the final text.
func (w *routedWorker) HandleCommand(
	ctx context.Context,
	command protocol.Command,
	payload json.RawMessage,
) (any, error) {
	switch command {
	case protocol.CommandRunTurn:
		return w.runTurn(ctx, payload)
	case protocol.CommandCancel:
		return protocol.CancelResult{CancelRequested: true}, nil
	case protocol.CommandSignalJob:
		return w.signalJob(payload)
	case protocol.CommandShutdown:
		return protocol.Ack{}, nil
	default:
		return nil, commerr.ErrNotImplemented
	}
}

func (w *routedWorker) runTurn(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request := protocol.RunTurn{}
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}

	w.turns = append(w.turns, request)

	store := w.connection.Store()

	lease, err := store.AcquireTurn(ctx, w.sessionID, session.StartTurnInput{
		RequestID: request.RequestID,
		Workspace: w.workspace,
	})
	if err != nil {
		return nil, err
	}

	if err := store.AppendCheckpoint(
		ctx,
		lease,
		[]session.MessageInput{{
			ID:        uuid.New(),
			Workspace: w.workspace,
			Role:      models.MessageRoleAssistant,
			Content:   routedAnswer,
			ModelID:   "provider/model",
		}},
		[]session.EventInput{{
			ID:          uuid.New(),
			RequestID:   request.RequestID,
			EventType:   routedEventType,
			PayloadJSON: `{"text":"` + routedAnswer + `"}`,
		}},
	); err != nil {
		return nil, err
	}

	// ReleaseTurn takes no context by design: it drops the local lease and
	// tells the controller, which is the same shape the production runtime
	// uses at the end of a turn.
	//nolint:contextcheck // The lease release carries no context.
	store.ReleaseTurn(lease)

	return protocol.TurnResult{Text: routedAnswer}, nil
}

// routedLauncher connects a routedWorker instead of starting a process.
type routedLauncher struct {
	t         *testing.T
	workers   []*routedWorker
	workspace string
}

func (l *routedLauncher) Kind() worker.Kind {
	return worker.KindNative
}

//nolint:ireturn // The Launcher contract is returning a Process.
func (l *routedLauncher) Launch(
	ctx context.Context,
	request worker.LaunchRequest,
) (worker.Process, error) {
	built := &routedWorker{
		document:  request.Document,
		sessionID: request.Document.SessionID,
		workspace: l.workspace,
	}

	connection, err := protocol.Dial(ctx, request.Document, built)
	if err != nil {
		return nil, err
	}

	built.connection = connection
	l.workers = append(l.workers, built)

	serveCtx := context.WithoutCancel(ctx)

	go func() {
		if err := connection.Serve(serveCtx); err != nil {
			l.t.Logf("routed worker stopped serving: %v", err)
		}
	}()

	return &routedProcess{connection: connection}, nil
}

type routedProcess struct {
	connection *protocol.Connection
}

func (p *routedProcess) Describe() worker.Descriptor {
	return worker.Descriptor{ProcessID: 1}
}

func (p *routedProcess) Stop(context.Context) error {
	p.connection.Close()

	return nil
}

func (p *routedProcess) Wait() (int, error) {
	return 0, nil
}

// publishedEvents records what the controller handed to the live feed.
type publishedEvents struct {
	events []session.EventInput
}

func (p *publishedEvents) PublishSessionEvents(
	_ context.Context,
	_ uuid.UUID,
	events []session.EventInput,
) {
	p.events = append(p.events, events...)
}

type routingFixture struct {
	router    *control.TurnRouter
	store     *session.Store
	launcher  *routedLauncher
	published *publishedEvents
	sessionID uuid.UUID
}

// newRoutingFixture wires the real registry, supervisor, and router.
//
// These tests do not call t.Parallel. db.Open installs the generated
// repositories as a package default, which is process-global state, so two
// fixtures opening a database at once race.
func newRoutingFixture(t *testing.T) routingFixture {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	root := canonicalTempDir(t)

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	profiles, err := worker.NewProfileSet(
		[]worker.Profile{
			{Name: worker.ProfileNative, Kind: worker.KindNative, Revision: 1},
			{
				Name:     worker.ProfileDockerSandbox,
				Kind:     worker.KindDocker,
				Revision: 2,
				Image:    routedTestImageTag,
			},
		},
		worker.ProfileNative,
	)
	require.NoError(t, err)

	launcher := &routedLauncher{t: t, workspace: root}
	published := &publishedEvents{}

	supervised, err := supervisor.New(supervisor.Options{
		Store:    store,
		Profiles: profiles,
		Launchers: map[worker.Kind]worker.Launcher{
			worker.KindNative: launcher,
		},
		Publisher:       published,
		SocketRoot:      routingSocketRoot(t),
		ConfigDirectory: t.TempDir(),
		ReadyTimeout:    routingReadyWait,
		StopTimeout:     routingStopWait,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, supervised.StopAll(context.Background()))
	})

	registry, err := control.NewRegistry(control.RegistryOptions{
		Store:        store,
		Policy:       policy,
		Profiles:     profiles,
		Workers:      supervised,
		RootAgent:    testRootAgent,
		DefaultModel: testDefaultModel,
	})
	require.NoError(t, err)

	opened, err := registry.Open(t.Context(), root, worker.ProfileNative)
	require.NoError(t, err)

	router, err := control.NewTurnRouter(registry, supervised)
	require.NoError(t, err)

	return routingFixture{
		router:    router,
		store:     store,
		launcher:  launcher,
		published: published,
		sessionID: opened.Session.ID,
	}
}

// A public message reaches the session's worker, and the worker's answer comes
// back through the same path.
func TestRouterStartsAWorkerAndRunsTheTurnInIt(t *testing.T) {
	fixture := newRoutingFixture(t)

	requestID := uuid.New()

	result, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage},
		requestID,
	)
	require.NoError(t, err)

	assert.Equal(t, routedAnswer, result.Text)
	assert.Equal(t, fixture.sessionID, result.SessionID)

	require.Len(t, fixture.launcher.workers, 1)

	turns := fixture.launcher.workers[0].turns
	require.Len(t, turns, 1)
	assert.Equal(t, routedMessage, turns[0].Message)
	assert.Equal(t, requestID, turns[0].RequestID)
}

// TestRouterSignalsAJobInTheSessionWorker carries a job signal to the process
// that holds the job's process group.
//
// The controller's own job registry is empty, so a signal handled there records
// the request and stops nothing. The payload is decoded on the worker side on
// purpose: a command encoded twice still arrives and still answers, and only
// reading the fields back shows it.
func TestRouterSignalsAJobInTheSessionWorker(t *testing.T) {
	fixture := newRoutingFixture(t)

	// The worker starts with the first turn, so the session has one to signal.
	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)
	require.NoError(t, err)

	jobID := uuid.New()

	result, err := fixture.router.SignalSessionJob(
		t.Context(),
		fixture.sessionID,
		jobID,
		routedSignal,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Signalled)
	assert.Equal(t, jobID, result.JobId)
	assert.Equal(t, api.JobSignalResponseState(routedSignalState), result.State)

	require.Len(t, fixture.launcher.workers, 1)

	signals := fixture.launcher.workers[0].signals
	require.Len(t, signals, 1)
	assert.Equal(t, jobID, signals[0].JobID)
	assert.Equal(t, routedSignal, signals[0].Signal)
}

// A session with no live worker has no process group to reach, which the router
// reports as no response rather than an error so the caller can record the
// request against the durable row.
func TestRouterReportsNoWorkerForAJobSignal(t *testing.T) {
	fixture := newRoutingFixture(t)

	result, err := fixture.router.SignalSessionJob(
		t.Context(),
		fixture.sessionID,
		uuid.New(),
		routedSignal,
	)
	require.NoError(t, err)
	assert.Nil(t, result)
}

// The worker's records land in the controller's database, because the worker
// holds no database handle of its own.
func TestRoutedTurnPersistsThroughTheController(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)
	require.NoError(t, err)

	page, err := fixture.store.ListMessages(
		t.Context(),
		fixture.sessionID,
		session.ListMessagesOptions{},
	)
	require.NoError(t, err)

	found := false

	for _, stored := range page.Items {
		if stored.Content == routedAnswer {
			found = true
		}
	}

	assert.True(t, found, "the worker's message must reach the controller DB")
}

// Events reach the live feed only after the controller has written them.
func TestRoutedTurnPublishesOnlyDurableEvents(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)
	require.NoError(t, err)

	require.NotEmpty(
		t,
		fixture.published.events,
		"the worker's events must reach the live feed",
	)

	stored, err := fixture.store.ListEvents(
		t.Context(),
		fixture.sessionID,
		session.ListEventsOptions{},
	)
	require.NoError(t, err)

	durable := map[string]bool{}
	for _, event := range stored.Items {
		durable[event.EventType] = true
	}

	for _, published := range fixture.published.events {
		assert.True(
			t,
			durable[published.EventType],
			"event %q was published without being written first",
			published.EventType,
		)
	}
}

// A second message reuses the same worker rather than launching another.
func TestRouterReusesTheSessionWorker(t *testing.T) {
	fixture := newRoutingFixture(t)

	for range 2 {
		_, err := fixture.router.RunSessionMessage(
			t.Context(),
			fixture.sessionID,
			agent.MessageRequest{Message: routedMessage},
			uuid.New(),
		)
		require.NoError(t, err)
	}

	require.Len(t, fixture.launcher.workers, 1)
	assert.Len(t, fixture.launcher.workers[0].turns, 2)
}

// A message for a session this controller does not have fails before any
// worker is launched.
func TestRouterRefusesAnUnknownSession(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		uuid.New(),
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)

	require.ErrorIs(t, err, commerr.ErrNotFound)
	assert.Empty(t, fixture.launcher.workers)
}

// A session whose stored profile is a Docker profile is refused outright on a
// controller with no Docker socket, and never falls back to a native worker.
func TestRouterRefusesADockerProfileWithoutAuthority(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.store.ReconfigureExecutionProfile(
		t.Context(),
		fixture.sessionID,
		worker.ProfileDockerSandbox,
		"isolating the workspace",
	)
	require.NoError(t, err)

	_, err = fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)

	require.ErrorIs(t, err, worker.ErrDockerAuthorityUnavailable)
	assert.Empty(
		t,
		fixture.launcher.workers,
		"a refused Docker profile must not start a native worker instead",
	)
}

// routingSocketRoot keeps a session socket inside the platform's sockaddr
// length limit, which a nested temporary directory would overrun.
func routingSocketRoot(t *testing.T) string {
	t.Helper()

	root := filepath.Join(os.TempDir(), "peenr-"+uuid.NewString()[:8])
	require.NoError(t, os.MkdirAll(root, socketRootMode))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })

	return root
}
