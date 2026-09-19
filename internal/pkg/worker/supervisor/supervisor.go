// Package supervisor owns the lifecycle of one controller's session workers.
//
// It is the only place a worker is created, so the profile policy cannot be
// bypassed, no worker runs without a durable generation row, and no worker is
// accepted without the credential this controller issued for that exact
// generation.
package supervisor

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

const (
	// defaultReadyTimeout bounds how long a launch waits for the worker to
	// register. A worker that has not connected by then is treated as failed
	// and its generation is recorded that way.
	defaultReadyTimeout = 60 * time.Second

	// defaultStopTimeout bounds a graceful stop before the launcher is asked
	// to end the worker outright.
	defaultStopTimeout = 20 * time.Second
)

// Options builds a supervisor.
type Options struct {
	Store    *session.Store
	Profiles *worker.ProfileSet

	// Launchers holds one launcher per kind. A kind with no launcher cannot
	// run: a Docker profile on a controller with no Docker socket is refused
	// rather than run somewhere else.
	Launchers map[worker.Kind]worker.Launcher

	// Publisher receives durable records after the controller writes them.
	Publisher protocol.Publisher

	// SocketRoot is the controller's private worker socket directory. It is
	// separate from the SQLite directory and from the public API.
	SocketRoot string

	// ConfigDirectory is the global Peen configuration every worker reads.
	ConfigDirectory string

	ReadyTimeout time.Duration
	StopTimeout  time.Duration
}

// Supervisor holds one live worker per session.
type Supervisor struct {
	options Options

	mutex    sync.Mutex
	sessions map[uuid.UUID]*supervised
}

// supervised is one session's live worker and the machinery behind it.
type supervised struct {
	generationID uuid.UUID
	listener     *protocol.Listener
	process      worker.Process
	ready        chan struct{}
	readyOnce    sync.Once
	profile      worker.Profile
}

// New validates the supervisor's dependencies.
func New(options Options) (*Supervisor, error) {
	if options.Store == nil || options.Profiles == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"worker supervisor dependency",
		)
	}

	if options.SocketRoot == "" || options.ConfigDirectory == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"worker supervisor socket root and configuration directory",
		)
	}

	if err := worker.ValidateSocketRoot(options.SocketRoot); err != nil {
		return nil, ctxerrors.Wrap(err, "validate the worker socket root")
	}

	if options.ReadyTimeout <= 0 {
		options.ReadyTimeout = defaultReadyTimeout
	}

	if options.StopTimeout <= 0 {
		options.StopTimeout = defaultStopTimeout
	}

	if options.Launchers == nil {
		options.Launchers = map[worker.Kind]worker.Launcher{}
	}

	return &Supervisor{
		options:  options,
		sessions: map[uuid.UUID]*supervised{},
	}, nil
}

// Profiles exposes the deployment's allowed profiles for reporting.
func (s *Supervisor) Profiles() *worker.ProfileSet {
	return s.options.Profiles
}

// EnsureRequest asks for one session's live worker.
type EnsureRequest struct {
	SessionID   uuid.UUID
	Workspace   string
	ProfileName string
}

// Ensure returns the session's registered worker, launching one when the
// session has none.
//
// A session keeps its worker across turns. A worker whose connection ended is
// replaced by a new generation rather than reused, and the replacement is
// recorded before it runs.
func (s *Supervisor) Ensure(
	ctx context.Context,
	request EnsureRequest,
) (*protocol.Worker, error) {
	if request.SessionID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"worker session",
		)
	}

	if live, found := s.live(request.SessionID); found {
		return live, nil
	}

	profile, err := s.options.Profiles.Select(request.ProfileName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "select the execution profile")
	}

	launcher, err := s.launcherFor(profile)
	if err != nil {
		return nil, err
	}

	return s.launch(ctx, request, profile, launcher)
}

// launcherFor resolves the launcher a profile needs, refusing plainly when the
// controller lacks the authority that profile requires.
//
//nolint:ireturn // Selecting between launch forms is the point.
func (s *Supervisor) launcherFor(
	profile worker.Profile,
) (worker.Launcher, error) {
	launcher, found := s.options.Launchers[profile.Kind]
	if found {
		return launcher, nil
	}

	if profile.RequiresDockerAuthority() {
		return nil, ctxerrors.Wrapf(
			worker.ErrDockerAuthorityUnavailable,
			"execution profile %q requires a controller Docker socket",
			profile.Name,
		)
	}

	return nil, ctxerrors.Wrapf(
		commerr.ErrNotImplemented,
		"no launcher is registered for %q workers",
		profile.Kind,
	)
}

// launch records the generation, opens its socket, starts the worker, and
// waits for it to register.
func (s *Supervisor) launch(
	ctx context.Context,
	request EnsureRequest,
	profile worker.Profile,
	launcher worker.Launcher,
) (*protocol.Worker, error) {
	document, err := s.recordGeneration(ctx, request, profile)
	if err != nil {
		return nil, err
	}

	held := &supervised{
		generationID: document.GenerationID,
		ready:        make(chan struct{}),
		profile:      profile,
	}

	s.mutex.Lock()
	s.sessions[request.SessionID] = held
	s.mutex.Unlock()

	registered, err := s.start(ctx, request, profile, launcher, held, document)
	if err != nil {
		s.abandon(ctx, request.SessionID, held, err)

		return nil, err
	}

	return registered, nil
}

// recordGeneration mints the credential and writes the generation row before
// anything launches, so a controller that dies mid-launch still leaves a
// generation an operator can reconcile.
func (s *Supervisor) recordGeneration(
	ctx context.Context,
	request EnsureRequest,
	profile worker.Profile,
) (worker.LaunchDocument, error) {
	credential, err := worker.NewCredential()
	if err != nil {
		return worker.LaunchDocument{}, ctxerrors.Wrap(
			err,
			"mint the worker credential",
		)
	}

	generationID := uuid.New()
	socketPath := worker.SocketPathFor(
		s.options.SocketRoot,
		request.SessionID,
	)

	if _, err := s.options.Store.CreateWorkerGeneration(
		ctx,
		request.SessionID,
		session.CreateWorkerGenerationInput{
			ID:              generationID,
			Kind:            string(profile.Kind),
			Profile:         profile.Name,
			ProfileRevision: profile.Revision,
			State:           string(worker.StateRequested),
			Workspace:       request.Workspace,
			SocketPath:      socketPath,
			CredentialHash:  credential.Hash(),
		},
	); err != nil {
		return worker.LaunchDocument{}, ctxerrors.Wrap(
			err,
			"record the worker generation",
		)
	}

	return worker.LaunchDocument{
		SessionID:       request.SessionID,
		GenerationID:    generationID,
		Credential:      credential,
		SocketPath:      socketPath,
		Workspace:       request.Workspace,
		ConfigDirectory: s.options.ConfigDirectory,
		Profile:         profile.Name,
	}, nil
}

// start opens the socket, launches the worker, and waits for registration.
func (s *Supervisor) start(
	ctx context.Context,
	request EnsureRequest,
	profile worker.Profile,
	launcher worker.Launcher,
	held *supervised,
	document worker.LaunchDocument,
) (*protocol.Worker, error) {
	listener, err := protocol.Listen(
		ctx,
		document.SocketPath,
		request.SessionID,
		s,
		s.options.Store,
		s.options.Publisher,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the worker socket")
	}

	held.listener = listener

	serveCtx := context.WithoutCancel(ctx)

	go func() {
		if err := listener.Serve(serveCtx); err != nil {
			ctxscope.GetLogger(serveCtx).Error(
				"worker socket stopped serving",
				"session_id", request.SessionID.String(),
				"err", err,
			)
		}
	}()

	if err := s.options.Store.UpdateWorkerGenerationState(
		ctx,
		held.generationID,
		string(worker.StateStarting),
		"",
	); err != nil {
		return nil, ctxerrors.Wrap(err, "mark the worker generation starting")
	}

	process, err := s.runLauncher(ctx, launcher, document, profile)
	if err != nil {
		return nil, err
	}

	held.process = process

	if err := s.recordIdentity(ctx, held, process.Describe()); err != nil {
		return nil, err
	}

	return s.awaitRegistration(ctx, request.SessionID, held)
}

// runLauncher starts the worker.
//
// Every launch form hands the document to the worker over a pipe, so the
// supervisor never writes a credential to disk for either one.
//
//nolint:ireturn // The Launcher contract is returning a Process.
func (s *Supervisor) runLauncher(
	ctx context.Context,
	launcher worker.Launcher,
	document worker.LaunchDocument,
	profile worker.Profile,
) (worker.Process, error) {
	process, err := launcher.Launch(ctx, worker.LaunchRequest{
		Document: document,
		Profile:  profile,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "launch the session worker")
	}

	return process, nil
}

func (s *Supervisor) recordIdentity(
	ctx context.Context,
	held *supervised,
	descriptor worker.Descriptor,
) error {
	if descriptor.ProcessID != 0 {
		if err := s.options.Store.RecordWorkerProcess(
			ctx,
			held.generationID,
			descriptor.ProcessID,
		); err != nil {
			return ctxerrors.Wrap(err, "record the worker process")
		}
	}

	if descriptor.ContainerID == "" {
		return nil
	}

	if err := s.options.Store.RecordWorkerContainer(
		ctx,
		held.generationID,
		descriptor.ContainerID,
		descriptor.ImageDigest,
	); err != nil {
		return ctxerrors.Wrap(err, "record the worker container")
	}

	return nil
}

// awaitRegistration waits for the worker to connect and prove its credential.
func (s *Supervisor) awaitRegistration(
	ctx context.Context,
	sessionID uuid.UUID,
	held *supervised,
) (*protocol.Worker, error) {
	timer := time.NewTimer(s.options.ReadyTimeout)
	defer timer.Stop()

	select {
	case <-held.ready:
	case <-timer.C:
		return nil, ctxerrors.Wrapf(
			worker.ErrWorkerLaunchFailed,
			"the worker did not register within %s",
			s.options.ReadyTimeout,
		)
	case <-ctx.Done():
		return nil, ctxerrors.Wrap(ctx.Err(), "await worker registration")
	}

	registered, found := held.listener.Connected()
	if !found {
		return nil, ctxerrors.Wrap(
			worker.ErrWorkerLaunchFailed,
			"the worker disconnected before it could run work",
		)
	}

	if err := s.options.Store.UpdateWorkerGenerationState(
		ctx,
		held.generationID,
		string(worker.StateReady),
		"",
	); err != nil {
		return nil, ctxerrors.Wrap(err, "mark the worker generation ready")
	}

	ctxscope.GetLogger(ctx).Info(
		"session worker ready",
		"session_id", sessionID.String(),
		"generation_id", held.generationID.String(),
		"profile", held.profile.Name,
		"kind", string(held.profile.Kind),
	)

	return registered, nil
}

// AcceptWorker verifies one registration against the durable generation.
//
// A worker is accepted only when this controller recorded that session, that
// generation, and that credential hash, and the generation has not already
// ended. It is what stops a worker replaying another generation's credential.
func (s *Supervisor) AcceptWorker(
	ctx context.Context,
	hello protocol.Hello,
) (protocol.HelloAck, error) {
	generation, err := s.options.Store.GetWorkerGeneration(
		ctx,
		hello.SessionID,
		hello.GenerationID,
	)
	if err != nil {
		return protocol.HelloAck{}, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"no such worker generation for this session",
		)
	}

	if worker.State(generation.State).Terminal() {
		return protocol.HelloAck{}, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"this worker generation has already ended",
		)
	}

	if !hello.Credential.Matches(generation.CredentialHash) {
		return protocol.HelloAck{}, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"the worker credential does not match this generation",
		)
	}

	stored, err := s.options.Store.Get(ctx, hello.SessionID)
	if err != nil {
		return protocol.HelloAck{}, ctxerrors.Wrap(
			err,
			"load the session for worker registration",
		)
	}

	s.markReady(hello.SessionID, hello.GenerationID)

	return protocol.HelloAck{
		SessionID: stored.ID,
		Workspace: stored.Workspace,
		Profile:   generation.Profile,
	}, nil
}

// markReady releases the launch that is waiting for this generation.
func (s *Supervisor) markReady(sessionID, generationID uuid.UUID) {
	s.mutex.Lock()
	held, found := s.sessions[sessionID]
	s.mutex.Unlock()

	if !found || held.generationID != generationID {
		return
	}

	held.readyOnce.Do(func() { close(held.ready) })
}

// live returns a session's worker when one is still connected.
func (s *Supervisor) live(sessionID uuid.UUID) (*protocol.Worker, bool) {
	s.mutex.Lock()
	held, found := s.sessions[sessionID]
	s.mutex.Unlock()

	if !found || held.listener == nil {
		return nil, false
	}

	return held.listener.Connected()
}

// Current reports a session's live worker without launching one.
func (s *Supervisor) Current(sessionID uuid.UUID) (*protocol.Worker, bool) {
	return s.live(sessionID)
}

// EnsureSessionWorker is Ensure in the shape the control surface routes with.
//
// The workspace and profile come from the stored session, so a client naming a
// session cannot also choose where it runs or what it may do.
func (s *Supervisor) EnsureSessionWorker(
	ctx context.Context,
	sessionID uuid.UUID,
	workspace string,
	profileName string,
) (*protocol.Worker, error) {
	return s.Ensure(ctx, EnsureRequest{
		SessionID:   sessionID,
		Workspace:   workspace,
		ProfileName: profileName,
	})
}

// abandon records a failed launch and releases everything it opened.
func (s *Supervisor) abandon(
	ctx context.Context,
	sessionID uuid.UUID,
	held *supervised,
	cause error,
) {
	logger := ctxscope.GetLogger(ctx)

	s.mutex.Lock()

	if s.sessions[sessionID] == held {
		delete(s.sessions, sessionID)
	}

	s.mutex.Unlock()

	if held.process != nil {
		stopCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			s.options.StopTimeout,
		)
		defer cancel()

		if err := held.process.Stop(stopCtx); err != nil {
			logger.Warn(
				"stopping a failed worker did not succeed",
				"reason", "worker_stop_failed",
				"session_id", sessionID.String(),
				"err", err,
			)
		}
	}

	if held.listener != nil {
		held.listener.Close()
	}

	if err := s.options.Store.UpdateWorkerGenerationState(
		context.WithoutCancel(ctx),
		held.generationID,
		string(worker.StateFailed),
		cause.Error(),
	); err != nil {
		logger.Error(
			"recording a failed worker generation failed",
			"session_id", sessionID.String(),
			"generation_id", held.generationID.String(),
			"err", err,
		)
	}
}

// Stop ends one session's worker and records the transition. Stopping a
// session that has none is not an error.
func (s *Supervisor) Stop(ctx context.Context, sessionID uuid.UUID) error {
	s.mutex.Lock()
	held, found := s.sessions[sessionID]

	if found {
		delete(s.sessions, sessionID)
	}

	s.mutex.Unlock()

	if !found {
		return nil
	}

	return s.stopHeld(ctx, sessionID, held)
}

func (s *Supervisor) stopHeld(
	ctx context.Context,
	sessionID uuid.UUID,
	held *supervised,
) error {
	logger := ctxscope.GetLogger(ctx)

	if err := s.options.Store.UpdateWorkerGenerationState(
		ctx,
		held.generationID,
		string(worker.StateStopping),
		"",
	); err != nil {
		logger.Warn(
			"marking a worker generation stopping failed",
			"reason", "worker_state_write_failed",
			"session_id", sessionID.String(),
			"err", err,
		)
	}

	stopCtx, cancel := context.WithTimeout(ctx, s.options.StopTimeout)
	defer cancel()

	// Asking the worker to finish over the protocol comes first, so it can
	// release its turn and record a final transition before the process ends.
	if registered, connected := held.listener.Connected(); connected {
		if err := registered.Shutdown(stopCtx); err != nil {
			logger.Debug(
				"the worker did not answer the shutdown command",
				"session_id", sessionID.String(),
				"err", err,
			)
		}
	}

	var stopErr error

	if held.process != nil {
		if err := held.process.Stop(stopCtx); err != nil {
			stopErr = ctxerrors.Wrap(err, "stop the session worker")
		}

		s.recordExit(ctx, held)
	}

	held.listener.Close()

	if err := s.options.Store.UpdateWorkerGenerationState(
		ctx,
		held.generationID,
		string(worker.StateStopped),
		"",
	); err != nil {
		return ctxerrors.Wrap(err, "mark the worker generation stopped")
	}

	return stopErr
}

func (s *Supervisor) recordExit(ctx context.Context, held *supervised) {
	code, err := held.process.Wait()
	if err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"reading the worker exit status failed",
			"generation_id", held.generationID.String(),
			"err", err,
		)

		return
	}

	if err := s.options.Store.RecordWorkerExit(
		ctx,
		held.generationID,
		code,
	); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"recording the worker exit status failed",
			"reason", "worker_exit_write_failed",
			"generation_id", held.generationID.String(),
			"err", err,
		)
	}
}

// StopAll ends every supervised worker, reporting the first failure while
// still stopping the rest.
func (s *Supervisor) StopAll(ctx context.Context) error {
	s.mutex.Lock()
	sessions := make([]uuid.UUID, 0, len(s.sessions))

	for sessionID := range s.sessions {
		sessions = append(sessions, sessionID)
	}

	s.mutex.Unlock()

	var firstErr error

	for _, sessionID := range sessions {
		if err := s.Stop(ctx, sessionID); err != nil {
			ctxscope.GetLogger(ctx).Warn(
				"stopping a session worker failed, continuing",
				"reason", "shutdown_continues",
				"session_id", sessionID.String(),
				"err", err,
			)

			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}
