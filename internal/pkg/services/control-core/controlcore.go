// Package controlcore opens the control surface's durable state and runtime.
//
// It creates no workspace session. A controller starts empty and gains a
// session only when a client opens a workspace through the control API.
package controlcore

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/gonfiguration"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/auditlog"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/nondumpable"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/controlspace"
	dockerworker "github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
)

// ServiceName is the servicepack registration key and log scope.
const ServiceName = control.CoreServiceName

// buildVersionScopeKey is where the binary stamps its release version at
// startup. The worker image tracks it, so a release runs the worker published
// beside it.
const buildVersionScopeKey = "version"

type serviceDependencies struct {
	enableNonDumpable func(context.Context) error
	parseConfig       func() (peenconfig.Config, error)
	configureAuditLog func() error
	auditLogDirectory func() (string, error)
	driverFactory     agent.DriverFactory
	handoff           *control.Handoff
}

// ControlCore owns the controller's configuration, SQLite state, provider
// registry, workspace policy, and agent runtime. control-api serves what this
// service opens.
type ControlCore struct {
	dependencies serviceDependencies

	readyOnce sync.Once
	ready     chan struct{}
}

// New builds the service through servicepack's generated registration.
func New() (*ControlCore, error) {
	return newControlCore(serviceDependencies{}), nil
}

func newControlCore(dependencies serviceDependencies) *ControlCore {
	if dependencies.enableNonDumpable == nil {
		dependencies.enableNonDumpable = nondumpable.Enable
	}

	if dependencies.parseConfig == nil {
		dependencies.parseConfig = peenconfig.Parse
	}

	if dependencies.configureAuditLog == nil {
		dependencies.configureAuditLog = auditlog.Configure
	}

	if dependencies.auditLogDirectory == nil {
		dependencies.auditLogDirectory = configuredAuditLogDirectory
	}

	if dependencies.handoff == nil {
		dependencies.handoff = control.SharedHandoff()
	}

	return &ControlCore{
		dependencies: dependencies,
		ready:        make(chan struct{}),
	}
}

func (s *ControlCore) Name() string {
	return ServiceName
}

// Ready closes once the durable store, provider registry, and session registry
// are open. control-api waits for it, so an auto-starting client never reaches
// an API whose state is still missing.
func (s *ControlCore) Ready() <-chan struct{} {
	return s.ready
}

// Run opens the controller's state, publishes it for control-api, and holds it
// until the context ends.
//
//nolint:funlen // Keep lock and core cleanup in their ownership order.
func (s *ControlCore) Run(ctx context.Context) (runErr error) {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))
	logger := ctxscope.GetLogger(ctx)

	// First, before any worker exists: a control agent runs as this user and
	// must not read the API token or provider keys from this process's /proc
	// files.
	if err := s.dependencies.enableNonDumpable(ctx); err != nil {
		return ctxerrors.Wrap(err, "protect the controller process")
	}

	config, err := s.dependencies.parseConfig()
	if err != nil {
		return ctxerrors.Wrap(err, "load control configuration")
	}

	// Registered before the core opens, so it is released only after the
	// deferred close below has stopped every worker and closed SQLite.
	releaseState, err := control.LockStateDirectory(config.StateDirectory)
	if err != nil {
		return ctxerrors.Wrap(err, "own the controller state")
	}

	defer releaseState(ctx)

	if err := s.dependencies.configureAuditLog(); err != nil {
		return ctxerrors.Wrap(err, "configure durable audit logging")
	}

	upstreams, err := config.Upstreams()
	if err != nil {
		return ctxerrors.Wrap(err, "load configured providers")
	}

	logValidatedConfig(ctx, config, upstreams)

	core, err := s.openCore(ctx, config, upstreams)
	if err != nil {
		return err
	}

	defer func() {
		runErr = errors.Join(runErr, s.closeCore(ctx, core))
	}()

	if err := s.dependencies.handoff.Publish(core); err != nil {
		return ctxerrors.Wrap(err, "publish control core")
	}

	s.readyOnce.Do(func() { close(s.ready) })

	logger.Info(
		"control core ready",
		"config_directory", config.ConfigDirectory,
		"state_directory", config.StateDirectory,
		"workspace_roots", core.Sessions.Roots(),
		"agent_spaces", core.AgentSpaces.Names(),
		"default_agent_space", core.AgentSpaces.Default(),
		"provider_count", len(upstreams),
		"default_model", config.DefaultModel,
	)

	<-ctx.Done()

	logger.Info("control core context cancelled")

	return nil
}

// openCore builds every durable dependency the controller serves from.
//
//nolint:funlen // One construction step per dependency, in dependency order.
func (s *ControlCore) openCore(
	ctx context.Context,
	config peenconfig.Config,
	upstreams []peenconfig.Upstream,
) (*control.Core, error) {
	metricRegistry := metrics.New()

	// Provider discovery stays here because it is the one part that genuinely
	// differs between a deployment and an embedding Go program: a deployment
	// discovers models from its configured providers, an embedder supplies
	// its own.
	models, err := agent.NewRegistry(ctx, agent.RegistryOptions{
		Upstreams:        upstreams,
		DefaultModel:     config.DefaultModel,
		CompactionModel:  config.CompactionModel,
		MaxContextTokens: config.MaxContextTokens,
		Factory:          s.dependencies.driverFactory,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "discover configured provider models")
	}

	//nolint:contextcheck // A startup probe bounded by its own timeout.
	agentSpaces, err := config.AgentSpaces()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load agent spaces")
	}

	harnessOptions, err := agent.HarnessOptions(config)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load harness options")
	}

	assembled, err := agent.Assemble(ctx, agent.AssembleOptions{
		Runtime:        runtimeOptions(config, models, metricRegistry),
		StateDirectory: config.StateDirectory,
		HarnessLimits:  harnessLimits(),
		HarnessOptions: harnessOptions,
		StoreOptions: session.Options{
			MaxStoredMessageBytes: config.MaxStoredMessageBytes,
		},
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "assemble agent runtime")
	}

	// The pump is created here, over this service's own context, so its
	// per-session goroutines stop when the service does. control-api
	// registers the live delivery path on it later. The store signals it
	// after every write that allocates feed sequences, which is what makes
	// every worker event durable before any client sees it.
	feed := control.NewFeedPump(ctx, assembled.Store)
	assembled.Store.SetFeedSignal(feed.Signal)

	//nolint:contextcheck // A startup probe bounded by its own timeout.
	workers, err := newWorkerSupervisor(
		config,
		upstreams,
		assembled.Store,
		assembled.Handle.Workflows,
		agentSpaces,
	)
	if err != nil {
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	auditLogDirectory, err := s.dependencies.auditLogDirectory()
	if err != nil {
		return nil, errors.Join(
			ctxerrors.Wrap(err, "load the audit log directory"),
			closeState(ctx, assembled.Handle),
		)
	}

	sessions, err := newSessionRegistry(
		config,
		auditLogDirectory,
		assembled.Store,
		agentSpaces,
		workers,
	)
	if err != nil {
		// The store is already open, so it has to be closed here rather than
		// left for the deferred shutdown that never gets a Core.
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	// The router watches the same pump the store signals, which is how it
	// learns each message was admitted and lets the next one in.
	turns, err := control.NewTurnRouter(sessions, workers, feed)
	if err != nil {
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	// The controller runs no model loop of its own, so a turn an event wakes
	// goes to the session's worker like a turn a client sends.
	assembled.Runtime.SetWakeRunner(func(
		ctx context.Context,
		sessionID uuid.UUID,
		request agent.MessageRequest,
	) error {
		if _, err := turns.RunSessionMessage(
			ctx,
			sessionID,
			request,
			uuid.New(),
		); err != nil {
			return ctxerrors.Wrap(err, "run the woken turn in its worker")
		}

		return nil
	})

	return &control.Core{
		Config:      config,
		Upstreams:   upstreams,
		Runtime:     assembled.Runtime,
		Store:       assembled.Store,
		Handle:      assembled.Handle,
		Sessions:    sessions,
		AgentSpaces: agentSpaces,
		Workers:     workers,
		Turns:       turns,
		Feed:        feed,
		Metrics:     metricRegistry,
	}, nil
}

// closeCore ends supervised work and closes SQLite. It runs after control-api
// has already stopped accepting requests, because servicepack stops services in
// reverse dependency order.
func (s *ControlCore) closeCore(ctx context.Context, core *control.Core) error {
	s.dependencies.handoff.Clear()

	return errors.Join(
		stopSessionJobs(ctx, core.Runtime),
		stopSessionWorkers(ctx, core.Workers),
		closeState(ctx, core.Handle),
	)
}

// newWorkerSupervisor wires the session worker supervisor.
//
// A Docker launcher is registered only when this controller can reach a Docker
// daemon. Without one, a Docker agent space is refused rather than run
// somewhere else, which is what keeps an isolation promise from quietly
// becoming a host process.
//
// A reachable daemon removes the control agent space, so Docker agent spaces
// are then the only ones offered. If their launcher cannot be built, startup
// fails with the reason: a controller that reports ready while no agent space
// can run is worse than one that refuses to start.
func newWorkerSupervisor(
	config peenconfig.Config,
	upstreams []peenconfig.Upstream,
	store *session.Store,
	workflows *db.WorkflowStorage,
	agentSpaces *worker.AgentSpaceSet,
) (*supervisor.Supervisor, error) {
	controlLauncher, err := controlspace.New(controlspace.Options{
		ProviderCredentialKeys: providerCredentialKeys(upstreams),
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the control worker launcher")
	}

	launchers := map[worker.Kind]worker.Launcher{
		worker.KindControl: controlLauncher,
	}

	imageOverride, imageFallback := resolveWorkerImages(config)

	if err := registerDockerLauncher(
		config,
		agentSpaces,
		imageOverride,
		imageFallback,
		launchers,
	); err != nil {
		return nil, err
	}

	built, err := supervisor.New(supervisor.Options{
		Store:           store,
		Workflows:       workflows,
		AgentSpaces:     agentSpaces,
		Launchers:       launchers,
		SocketRoot:      config.WorkerSocketRoot(),
		ConfigDirectory: config.ConfigDirectory,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker supervisor")
	}

	return built, nil
}

// providerCredentialKeys names the variables that hold the configured
// providers' credentials, which a control worker keeps for its model calls.
func providerCredentialKeys(upstreams []peenconfig.Upstream) []string {
	keys := make([]string, 0, len(upstreams))

	for _, upstream := range upstreams {
		if upstream.APIKeyEnv == "" {
			continue
		}

		keys = append(keys, upstream.APIKeyEnv)
	}

	return keys
}

// resolveWorkerImages decides which image a Docker worker runs.
//
// PEEN_WORKER_IMAGE, when set, replaces every profile's image. Without it a
// profile's own image applies, and a profile that names none runs the fallback:
// the image published alongside this build, which is what lets a release name
// its own worker without anyone writing an image into configuration. The
// version comes from the process scope the binary stamps at startup.
func resolveWorkerImages(config peenconfig.Config) (string, string) {
	override := strings.TrimSpace(config.WorkerImage)

	version, _ := ctxscope.GetGlobal()[buildVersionScopeKey].(string)

	return override, worker.ImageForBuildVersion(version)
}

// registerDockerLauncher adds the Docker launcher when this controller can
// reach a Docker daemon, and fails when it then cannot build one.
//
// Reaching a daemon removes the control agent space, so the Docker agent
// spaces are the only ones offered. A launcher failure here is not a degraded
// mode, it is a controller that can run nothing, and the error names the usual
// cause: a numeric `--user uid:gid` whose UID has no passwd entry in the image.
func registerDockerLauncher(
	config peenconfig.Config,
	agentSpaces *worker.AgentSpaceSet,
	imageOverride, imageFallback string,
	launchers map[worker.Kind]worker.Launcher,
) error {
	authority := config.DockerAuthority()
	if !authority.Present() {
		return nil
	}

	dockerLauncher, err := newDockerLauncher(
		config,
		authority,
		agentSpaces,
		imageOverride,
		imageFallback,
	)
	if err != nil {
		return ctxerrors.Wrapf(
			err,
			"the controller reaches the Docker daemon at %s, so only Docker "+
				"agent spaces are offered, but no Docker worker launcher "+
				"can be built; a controller run with a numeric --user needs "+
				"PEEN_HOST_USERNAME and PEEN_HOST_HOME",
			authority,
		)
	}

	launchers[worker.KindDocker] = dockerLauncher

	return nil
}

// newDockerLauncher builds the Docker worker launcher for a controller that
// reaches a Docker daemon.
//
// Peen launches workers only through a local socket, so a daemon reachable only
// over TCP fails here. A `docker: full` agent space whose socket group cannot
// be granted fails here too, so the refusal happens at startup.
func newDockerLauncher(
	config peenconfig.Config,
	authority peenconfig.DockerAuthority,
	agentSpaces *worker.AgentSpaceSet,
	imageOverride, imageFallback string,
) (*dockerworker.Launcher, error) {
	identity, err := dockerworker.CurrentHostIdentityWithOverride(
		config.HostUsername,
		config.HostHome,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"read the host identity for Docker workers",
		)
	}

	runtimeEnvironment, err := config.WorkerEnvironment()
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"build the Docker worker runtime environment",
		)
	}

	client, err := dockerworker.NewDaemonClient(authority.SocketPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the Docker daemon client")
	}

	socketGID := dockerworker.SocketGID(authority.SocketPath)

	if err := dockerworker.ValidateFullAccessSocket(
		agentSpaces,
		socketGID,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "validate the Docker socket grant")
	}

	launcher, err := dockerworker.New(dockerworker.Options{
		Client:             client,
		Identity:           identity,
		ImageOverride:      imageOverride,
		ImageFallback:      imageFallback,
		DockerSocketPath:   authority.SocketPath,
		DockerSocketGID:    socketGID,
		RuntimeEnvironment: runtimeEnvironment,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the Docker worker launcher")
	}

	return launcher, nil
}

// newSessionRegistry builds the workspace policy and session registry from
// deployment configuration.
//
// The policy protects every directory that holds controller data. A workspace
// is bind-mounted into a Docker worker, so a workspace root such as $HOME that
// also holds the state directory must not let a session open a directory that
// carries SQLite, the audit logs, or another session's worker socket.
func newSessionRegistry(
	config peenconfig.Config,
	auditLogDirectory string,
	store *session.Store,
	agentSpaces *worker.AgentSpaceSet,
	workers *supervisor.Supervisor,
) (*control.Registry, error) {
	roots, err := config.WorkspaceRoots()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load configured workspace roots")
	}

	policy, err := control.NewWorkspacePolicy(
		roots,
		control.WithProtectedDirectories(control.ProtectedDirectories{
			// PEEN_WORKER_SOCKET_DIR and PEEN_LOG_DIRECTORY default to
			// paths under the state directory, but either can be set to a
			// path outside it. An empty audit directory means the default.
			State: []string{
				config.StateDirectory,
				config.WorkerSocketRoot(),
				auditLogDirectory,
			},
			Configuration: []string{config.ConfigDirectory},
		}),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create workspace policy")
	}

	registry, err := control.NewRegistry(control.RegistryOptions{
		Store:        store,
		Policy:       policy,
		AgentSpaces:  agentSpaces,
		Workers:      workers,
		RootAgent:    config.Agent,
		DefaultModel: config.DefaultModel,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create control session registry")
	}

	return registry, nil
}

// configuredAuditLogDirectory reads PEEN_LOG_DIRECTORY the way the audit sink
// does. It is empty when unset, because the sink then writes under the state
// directory, which the workspace policy already protects.
func configuredAuditLogDirectory() (string, error) {
	config := auditlog.Config{}
	if err := gonfiguration.Parse(&config); err != nil {
		return "", ctxerrors.Wrap(err, "parse audit log configuration")
	}

	return config.Directory, nil
}

// closeState closes the durable store, reporting rather than hiding a failure
// so a corrupted shutdown does not look clean.
func closeState(ctx context.Context, handle *db.Handle) error {
	if err := handle.Close(); err != nil {
		wrapped := ctxerrors.Wrap(err, "close control state")
		ctxscope.GetLogger(ctx).Error(
			"control state close failed",
			"err", wrapped,
		)

		return wrapped
	}

	return nil
}

// stopSessionJobs ends every supervised process the runtime started.
// Supervised jobs are children of this process, so none may silently outlive
// it, and the shutdown context is detached because the context that ended the
// service is already cancelled by the time this runs.
func stopSessionJobs(ctx context.Context, runtime *agent.Runtime) error {
	shutdownCtx := context.WithoutCancel(ctx)

	if err := runtime.ShutdownJobs(shutdownCtx); err != nil {
		ctxscope.GetLogger(shutdownCtx).Error(
			"stopping session jobs failed",
			"err", err,
		)

		return ctxerrors.Wrap(err, "shut down session jobs")
	}

	return nil
}

// stopSessionWorkers ends every session worker after its jobs are gone, so a
// worker is not torn down under a process still running inside it.
//
// A Docker worker owns a real container, so leaving one running would leak it
// across a restart with a durable row still claiming the worker is ready.
func stopSessionWorkers(
	ctx context.Context,
	workers *supervisor.Supervisor,
) error {
	if workers == nil {
		return nil
	}

	shutdownCtx := context.WithoutCancel(ctx)

	if err := workers.StopAll(shutdownCtx); err != nil {
		ctxscope.GetLogger(shutdownCtx).Error(
			"stopping session workers failed",
			"err", err,
		)

		return ctxerrors.Wrap(err, "shut down session workers")
	}

	return nil
}

func (s *ControlCore) Stop(ctx context.Context) error {
	serviceCtx := ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))

	ctxscope.GetLogger(serviceCtx).Info("stopping control core")

	return nil
}
