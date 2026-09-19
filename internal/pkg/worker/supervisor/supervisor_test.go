package supervisor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProfileNative = worker.ProfileNative
	testProfileDocker = worker.ProfileDockerSandbox
	testDockerImage   = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	testSocketRootMode os.FileMode = 0o700
	testWrittenMessage             = "written through the worker protocol"
)

// fakeWorker is a worker that dials its controller in process. It proves the
// protocol and the supervisor without spawning anything, so these tests need
// no binary and no daemon.
type fakeWorker struct {
	connection *protocol.Connection
	document   worker.LaunchDocument
}

func (w *fakeWorker) HandleCall(
	context.Context,
	string,
	json.RawMessage,
) (any, error) {
	return nil, commerr.ErrNotImplemented
}

func (w *fakeWorker) HandleCommand(
	_ context.Context,
	command protocol.Command,
	_ json.RawMessage,
) (any, error) {
	switch command {
	case protocol.CommandRunTurn:
		return protocol.TurnResult{Text: "done"}, nil
	case protocol.CommandCancel:
		return protocol.CancelResult{CancelRequested: true}, nil
	case protocol.CommandSignalJob:
		return protocol.SignalJobResult{Handled: true, Signalled: true}, nil
	case protocol.CommandShutdown:
		return protocol.Ack{}, nil
	default:
		return nil, commerr.ErrNotImplemented
	}
}

// fakeLauncher captures the launch document and connects a fake worker, which
// is what makes the supervisor's readiness path observable.
type fakeLauncher struct {
	kind    worker.Kind
	workers []*fakeWorker
	// connect false leaves the worker unconnected, so a test can watch a
	// launch that never registers.
	connect  bool
	launched int
	t        *testing.T
}

func (l *fakeLauncher) Kind() worker.Kind {
	return l.kind
}

//nolint:ireturn // The Launcher contract is returning a Process.
func (l *fakeLauncher) Launch(
	ctx context.Context,
	request worker.LaunchRequest,
) (worker.Process, error) {
	l.launched++

	if !l.connect {
		return &fakeProcess{}, nil
	}

	built := &fakeWorker{document: request.Document}

	connection, err := protocol.Dial(ctx, request.Document, built)
	if err != nil {
		return nil, err
	}

	built.connection = connection
	l.workers = append(l.workers, built)

	serveCtx := context.WithoutCancel(ctx)

	go func() {
		if err := connection.Serve(serveCtx); err != nil {
			l.t.Logf("fake worker stopped serving: %v", err)
		}
	}()

	return &fakeProcess{connection: connection}, nil
}

type fakeProcess struct {
	connection *protocol.Connection
}

func (p *fakeProcess) Describe() worker.Descriptor {
	return worker.Descriptor{ProcessID: testProcessID}
}

func (p *fakeProcess) Stop(context.Context) error {
	if p.connection != nil {
		p.connection.Close()
	}

	return nil
}

func (p *fakeProcess) Wait() (int, error) {
	return 0, nil
}

const testProcessID = 4242

type fixture struct {
	supervisor *supervisor.Supervisor
	store      *session.Store
	launcher   *fakeLauncher
	sessionID  uuid.UUID
	workspace  string
}

// newFixture builds a supervisor over a real store and a real socket.
//
// These tests do not call t.Parallel. db.Open installs the generated
// repositories as a package default, which is process-global state, so two
// fixtures opening a database at once race.
func newFixture(t *testing.T, connect bool) fixture {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	opened, err := store.OpenWorkspace(
		t.Context(),
		t.TempDir(),
		session.OpenSessionOptions{
			RootAgent:        "peen",
			ModelID:          "provider/model",
			ExecutionProfile: testProfileNative,
		},
	)
	require.NoError(t, err)

	profiles, err := worker.NewProfileSet(
		[]worker.Profile{
			{Name: testProfileNative, Kind: worker.KindNative, Revision: 1},
			{
				Name:     testProfileDocker,
				Kind:     worker.KindDocker,
				Revision: 2,
				Image:    testDockerImage,
			},
		},
		testProfileNative,
	)
	require.NoError(t, err)

	launcher := &fakeLauncher{
		kind:    worker.KindNative,
		connect: connect,
		t:       t,
	}

	supervised, err := supervisor.New(supervisor.Options{
		Store:    store,
		Profiles: profiles,
		Launchers: map[worker.Kind]worker.Launcher{
			worker.KindNative: launcher,
		},
		SocketRoot:      socketRoot(t),
		ConfigDirectory: t.TempDir(),
		ReadyTimeout:    5 * time.Second,
		StopTimeout:     2 * time.Second,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, supervised.StopAll(context.Background()))
	})

	return fixture{
		supervisor: supervised,
		store:      store,
		launcher:   launcher,
		sessionID:  opened.Session.ID,
		workspace:  opened.Session.Workspace,
	}
}

func (f fixture) ensure(t *testing.T) *protocol.Worker {
	t.Helper()

	registered, err := f.supervisor.Ensure(
		t.Context(),
		supervisor.EnsureRequest{
			SessionID:   f.sessionID,
			Workspace:   f.workspace,
			ProfileName: testProfileNative,
		},
	)
	require.NoError(t, err)

	return registered
}

// workerStore is the durable surface the connected fake worker holds.
func (f fixture) workerStore(t *testing.T) *protocol.Store {
	t.Helper()

	require.NotEmpty(t, f.launcher.workers)

	return f.launcher.workers[0].connection.Store()
}

// A launch records the generation before anything starts, then marks it ready
// only once the worker has registered with its credential.
func TestSupervisorRecordsAGenerationThenMarksItReady(t *testing.T) {
	fixture := newFixture(t, true)

	require.NotNil(t, fixture.ensure(t))

	current, err := fixture.store.CurrentWorkerGeneration(
		t.Context(),
		fixture.sessionID,
	)
	require.NoError(t, err)

	assert.Equal(t, string(worker.StateReady), current.State)
	assert.Equal(t, testProfileNative, current.Profile)
	assert.Equal(t, int64(1), current.ProfileRevision)
	assert.Equal(t, testProcessID, current.ProcessID)
	require.NotNil(t, current.StartedAt)

	// The raw credential is never stored, only its verification hash.
	issued := string(fixture.launcher.workers[0].document.Credential)
	assert.NotEmpty(t, current.CredentialHash)
	assert.NotEqual(t, issued, current.CredentialHash)
}

// A session keeps one worker across turns rather than launching another.
func TestSupervisorReusesOneWorkerPerSession(t *testing.T) {
	fixture := newFixture(t, true)

	first := fixture.ensure(t)
	second := fixture.ensure(t)

	assert.Same(t, first, second)
	assert.Equal(t, 1, fixture.launcher.launched)
}

// A Docker profile on a controller with no Docker socket is refused outright.
// It never falls back to a native worker.
func TestSupervisorRefusesADockerProfileWithoutDockerAuthority(t *testing.T) {
	fixture := newFixture(t, true)

	_, err := fixture.supervisor.Ensure(
		t.Context(),
		supervisor.EnsureRequest{
			SessionID:   fixture.sessionID,
			Workspace:   fixture.workspace,
			ProfileName: testProfileDocker,
		},
	)

	require.ErrorIs(t, err, worker.ErrDockerAuthorityUnavailable)
	assert.Zero(
		t,
		fixture.launcher.launched,
		"a refused Docker profile must not launch a native worker instead",
	)

	_, err = fixture.store.CurrentWorkerGeneration(
		t.Context(),
		fixture.sessionID,
	)
	require.ErrorIs(
		t,
		err,
		commerr.ErrNotFound,
		"a refused profile must not record a generation",
	)
}

// A profile the operator never defined is refused before anything is recorded.
func TestSupervisorRefusesAnUndefinedProfile(t *testing.T) {
	fixture := newFixture(t, true)

	_, err := fixture.supervisor.Ensure(
		t.Context(),
		supervisor.EnsureRequest{
			SessionID:   fixture.sessionID,
			Workspace:   fixture.workspace,
			ProfileName: worker.ProfileDockerHostLike,
		},
	)

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Zero(t, fixture.launcher.launched)
}

// A worker that never connects is recorded as failed rather than left pending.
func TestSupervisorFailsAGenerationThatNeverRegisters(t *testing.T) {
	fixture := newFixture(t, false)

	_, err := fixture.supervisor.Ensure(
		t.Context(),
		supervisor.EnsureRequest{
			SessionID:   fixture.sessionID,
			Workspace:   fixture.workspace,
			ProfileName: testProfileNative,
		},
	)
	require.ErrorIs(t, err, worker.ErrWorkerLaunchFailed)

	current, err := fixture.store.CurrentWorkerGeneration(
		t.Context(),
		fixture.sessionID,
	)
	require.NoError(t, err)

	assert.Equal(t, string(worker.StateFailed), current.State)
	assert.NotEmpty(t, current.FailureDetail)
	require.NotNil(t, current.EndedAt)
}

// A worker cannot register with a credential the controller did not issue for
// that generation, and it cannot register for another session.
func TestRegistrationRefusesForgedLaunchDocuments(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(worker.LaunchDocument) worker.LaunchDocument
	}{
		{
			name: "replayed credential",
			mutate: func(
				document worker.LaunchDocument,
			) worker.LaunchDocument {
				document.Credential = worker.Credential("not-the-issued-one")

				return document
			},
		},
		{
			name: "another session",
			mutate: func(
				document worker.LaunchDocument,
			) worker.LaunchDocument {
				document.SessionID = uuid.New()

				return document
			},
		},
		{
			name: "another generation",
			mutate: func(
				document worker.LaunchDocument,
			) worker.LaunchDocument {
				document.GenerationID = uuid.New()

				return document
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, true)

			fixture.ensure(t)

			forged := tc.mutate(fixture.launcher.workers[0].document)

			_, err := protocol.Dial(t.Context(), forged, &fakeWorker{})

			require.ErrorIs(t, err, protocol.ErrWorkerRejected)
		})
	}
}

// A registered worker cannot reach another session's durable records. Its
// socket binds one session and every frame is checked against it.
func TestWorkerCannotReachAnotherSession(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	store := fixture.workerStore(t)
	foreign := uuid.New()

	_, readErr := store.Get(t.Context(), foreign)
	require.ErrorIs(t, readErr, commerr.ErrPermissionDenied)

	_, writeErr := store.CreateSessionNotice(
		t.Context(),
		foreign,
		session.CreateSessionNoticeInput{},
	)
	require.ErrorIs(t, writeErr, commerr.ErrPermissionDenied)
}

// A worker cannot claim a generation other than its own for a turn, which is
// what stops it relabelling work as having run somewhere it did not.
func TestWorkerCannotClaimAnotherGeneration(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	store := fixture.workerStore(t)

	lease, err := store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
		},
	)
	require.NoError(t, err)

	t.Cleanup(func() { store.ReleaseTurn(lease) })

	err = store.RecordTurnWorkerGeneration(t.Context(), lease, uuid.New())

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

// A worker may not perform a control-plane operation even over its own socket.
func TestWorkerCannotPerformControlPlaneOperations(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	store := fixture.workerStore(t)

	_, openErr := store.OpenWorkspace(
		t.Context(),
		fixture.workspace,
		session.OpenSessionOptions{},
	)
	require.ErrorIs(t, openErr, commerr.ErrPermissionDenied)

	_, generationErr := store.CreateWorkerGeneration(
		t.Context(),
		fixture.sessionID,
		session.CreateWorkerGenerationInput{},
	)
	require.ErrorIs(t, generationErr, commerr.ErrPermissionDenied)

	_, recoverErr := store.RecoverInterrupted(t.Context())
	require.ErrorIs(t, recoverErr, commerr.ErrPermissionDenied)
}

// A worker's durable writes land in the controller's database, which is the
// whole point of the protocol: the worker holds no handle of its own.
func TestWorkerWritesReachTheControllerDatabase(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	store := fixture.workerStore(t)

	lease, err := store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
		},
	)
	require.NoError(t, err)

	require.NoError(t, store.AppendCheckpoint(
		t.Context(),
		lease,
		[]session.MessageInput{{
			ID:        uuid.New(),
			Workspace: fixture.workspace,
			Role:      models.MessageRoleAssistant,
			Content:   testWrittenMessage,
			ModelID:   "provider/model",
		}},
		nil,
	))

	store.ReleaseTurn(lease)

	// Read it back through the controller's own store, not the worker's.
	page, err := fixture.store.ListMessages(
		t.Context(),
		fixture.sessionID,
		session.ListMessagesOptions{},
	)
	require.NoError(t, err)

	found := false

	for _, stored := range page.Items {
		if stored.Content == testWrittenMessage {
			found = true
		}
	}

	assert.True(t, found, "the worker's message must be in the controller DB")
}

// Releasing a turn frees the controller's lease, so the next turn is not
// refused as busy.
func TestWorkerTurnLeaseReleasesOnTheController(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	store := fixture.workerStore(t)

	first, err := store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
		},
	)
	require.NoError(t, err)

	assert.True(t, fixture.store.IsActive(fixture.sessionID))

	store.ReleaseTurn(first)

	assert.False(t, fixture.store.IsActive(fixture.sessionID))

	second, err := store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
		},
	)
	require.NoError(t, err)

	store.ReleaseTurn(second)
}

// Stopping a worker records the terminal transition and releases the socket.
func TestSupervisorStopRecordsTheTerminalState(t *testing.T) {
	fixture := newFixture(t, true)

	fixture.ensure(t)

	require.NoError(t, fixture.supervisor.Stop(t.Context(), fixture.sessionID))

	current, err := fixture.store.CurrentWorkerGeneration(
		t.Context(),
		fixture.sessionID,
	)
	require.NoError(t, err)

	assert.Equal(t, string(worker.StateStopped), current.State)
	require.NotNil(t, current.EndedAt)

	_, held := fixture.supervisor.Current(fixture.sessionID)
	assert.False(t, held)
}

// socketRoot keeps a session socket inside the platform's sockaddr length
// limit, which a nested temporary directory would overrun.
func socketRoot(t *testing.T) string {
	t.Helper()

	root := filepath.Join(os.TempDir(), "peen-"+uuid.NewString()[:8])
	require.NoError(t, os.MkdirAll(root, testSocketRootMode))
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(root))
	})

	return root
}
