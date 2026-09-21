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

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/auditlog"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	dockerworker "github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/psyb0t/peen/internal/pkg/worker/native"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
)

// ServiceName is the servicepack registration key and log scope.
const ServiceName = control.CoreServiceName

// buildVersionScopeKey is where the binary stamps its release version at
// startup. The worker image tracks it, so a release runs the worker published
// beside it.
const buildVersionScopeKey = "version"

// reasonDockerLauncherUnavailable marks a controller that reached a Docker
// socket but could not build the launcher behind it.
const reasonDockerLauncherUnavailable = "docker_launcher_unavailable"

type serviceDependencies struct {
	parseConfig       func() (peenconfig.Config, error)
	configureAuditLog func() error
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
	if dependencies.parseConfig == nil {
		dependencies.parseConfig = peenconfig.Parse
	}

	if dependencies.configureAuditLog == nil {
		dependencies.configureAuditLog = auditlog.Configure
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
func (s *ControlCore) Run(ctx context.Context) (runErr error) {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))
	logger := ctxscope.GetLogger(ctx)

	config, err := s.dependencies.parseConfig()
	if err != nil {
		return ctxerrors.Wrap(err, "load control configuration")
	}

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
		"execution_profiles", core.Profiles.Names(),
		"default_execution_profile", core.Profiles.Default(),
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

	profiles, err := config.ExecutionProfiles()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load execution profiles")
	}

	assembled, err := agent.Assemble(ctx, agent.AssembleOptions{
		Runtime:        runtimeOptions(config, models, metricRegistry),
		StateDirectory: config.StateDirectory,
		HarnessLimits:  harnessLimits(),
		StoreOptions: session.Options{
			MaxStoredMessageBytes: config.MaxStoredMessageBytes,
		},
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "assemble agent runtime")
	}

	// The relay is created here and given to the supervisor as its publisher,
	// then control-api registers the live delivery path on it. That ordering
	// is what makes every worker event durable before any client sees it.
	relay := control.NewEventRelay()

	workers, err := newWorkerSupervisor(
		ctx,
		config,
		assembled.Store,
		profiles,
		relay,
	)
	if err != nil {
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	sessions, err := newSessionRegistry(
		config,
		assembled.Store,
		profiles,
		workers,
	)
	if err != nil {
		// The store is already open, so it has to be closed here rather than
		// left for the deferred shutdown that never gets a Core.
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	turns, err := control.NewTurnRouter(sessions, workers)
	if err != nil {
		return nil, errors.Join(err, closeState(ctx, assembled.Handle))
	}

	return &control.Core{
		Config:    config,
		Upstreams: upstreams,
		Runtime:   assembled.Runtime,
		Store:     assembled.Store,
		Handle:    assembled.Handle,
		Sessions:  sessions,
		Profiles:  profiles,
		Workers:   workers,
		Turns:     turns,
		Events:    relay,
		Metrics:   metricRegistry,
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

// newSessionRegistry builds the workspace policy and session registry from
// deployment configuration.
// newWorkerSupervisor wires the session worker supervisor.
//
// A Docker launcher is registered only when this controller can actually reach
// a Docker socket AND can build a launcher for it. Without one, a Docker
// profile is refused rather than run somewhere else, which is what keeps an
// isolation promise from quietly becoming a host process.
//
// A socket that is present but unusable degrades the same way rather than
// failing startup. The common case is a controller started with numeric
// `--user uid:gid` whose UID has no passwd entry in the image, which leaves
// every native profile working and only Docker profiles refused.
func newWorkerSupervisor(
	ctx context.Context,
	config peenconfig.Config,
	store *session.Store,
	profiles *worker.ProfileSet,
	publisher protocol.Publisher,
) (*supervisor.Supervisor, error) {
	nativeLauncher, err := native.New(native.Options{})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the native worker launcher")
	}

	launchers := map[worker.Kind]worker.Launcher{
		worker.KindNative: nativeLauncher,
	}

	// A deployment override replaces every profile's own image, so it is
	// checked here rather than when a session first asks for a worker.
	workerImage := resolveWorkerImage(config)

	registerDockerLauncher(ctx, config, workerImage, launchers)

	built, err := supervisor.New(supervisor.Options{
		Publisher:       publisher,
		Store:           store,
		Profiles:        profiles,
		Launchers:       launchers,
		SocketRoot:      config.WorkerSocketRoot(),
		ConfigDirectory: config.ConfigDirectory,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker supervisor")
	}

	return built, nil
}

// resolveWorkerImage decides which image a Docker worker runs.
//
// An operator override wins. Otherwise the controller runs the worker image
// published alongside this build, which is what lets a release name its own
// worker without anyone writing an image into configuration. The version comes
// from the process scope the binary stamps at startup.
func resolveWorkerImage(config peenconfig.Config) string {
	if configured := strings.TrimSpace(config.WorkerImage); configured != "" {
		return configured
	}

	version, _ := ctxscope.GetGlobal()[buildVersionScopeKey].(string)

	return worker.ImageForBuildVersion(version)
}

// registerDockerLauncher adds the Docker launcher when this controller can
// both reach a Docker socket and build a launcher over it.
//
// A socket it cannot use leaves Docker profiles refused rather than failing
// startup, which is the same outcome as having no socket at all. Every other
// profile keeps working, so one unusable capability does not take down a
// controller whose sessions may never ask for it.
func registerDockerLauncher(
	ctx context.Context,
	config peenconfig.Config,
	workerImage string,
	launchers map[worker.Kind]worker.Launcher,
) {
	if !config.HasDockerAuthority() {
		return
	}

	dockerLauncher, err := newDockerLauncher(config, workerImage)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"docker worker launcher unavailable, docker profiles refused",
			"reason", reasonDockerLauncherUnavailable,
			"docker_socket_path", config.DockerSocketPath(),
			"err", err,
		)

		return
	}

	launchers[worker.KindDocker] = dockerLauncher
}

// newDockerLauncher builds the Docker worker launcher for a controller that
// has its own Docker socket.
//
// It is only reached when HasDockerAuthority already confirmed the socket, so a
// failure here is a real misconfiguration, such as a control account that
// cannot own worker files, rather than an absent daemon.
func newDockerLauncher(
	config peenconfig.Config,
	workerImage string,
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

	client, err := dockerworker.NewDaemonClient(config.DockerSocketPath())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the Docker daemon client")
	}

	launcher, err := dockerworker.New(dockerworker.Options{
		Client:             client,
		Identity:           identity,
		Image:              workerImage,
		DockerSocketGID:    dockerworker.SocketGID(config.DockerSocketPath()),
		RuntimeEnvironment: runtimeEnvironment,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the Docker worker launcher")
	}

	return launcher, nil
}

func newSessionRegistry(
	config peenconfig.Config,
	store *session.Store,
	profiles *worker.ProfileSet,
	workers *supervisor.Supervisor,
) (*control.Registry, error) {
	roots, err := config.WorkspaceRoots()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load configured workspace roots")
	}

	policy, err := control.NewWorkspacePolicy(roots)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create workspace policy")
	}

	registry, err := control.NewRegistry(control.RegistryOptions{
		Store:        store,
		Policy:       policy,
		Profiles:     profiles,
		Workers:      workers,
		RootAgent:    config.Agent,
		DefaultModel: config.DefaultModel,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create control session registry")
	}

	return registry, nil
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
