package httpserver

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	peenhttp "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	ServiceName      = "http-server"
	networkTCP       = "tcp"
	serveWorkerCount = 2
)

type serviceDependencies struct {
	parseConfig   func() (peenconfig.Config, error)
	driverFactory agent.DriverFactory
	listen        func(
		ctx context.Context,
		network string,
		address string,
	) (net.Listener, error)
}

// HTTPServer composes Peen's durable agent runtime with its client-facing API.
type HTTPServer struct {
	dependencies serviceDependencies
}

// New builds the HTTP service through servicepack's generated registration.
func New() (*HTTPServer, error) {
	return newHTTPServer(serviceDependencies{
		parseConfig: peenconfig.Parse,
		listen:      (&net.ListenConfig{}).Listen,
	}), nil
}

func newHTTPServer(dependencies serviceDependencies) *HTTPServer {
	if dependencies.parseConfig == nil {
		dependencies.parseConfig = peenconfig.Parse
	}

	if dependencies.listen == nil {
		dependencies.listen = (&net.ListenConfig{}).Listen
	}

	return &HTTPServer{dependencies: dependencies}
}

func (s *HTTPServer) Name() string {
	return ServiceName
}

//nolint:funlen // Paired server lifecycles belong in this method.
func (s *HTTPServer) Run(ctx context.Context) (runErr error) {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))
	logger := ctxscope.GetLogger(ctx)

	config, err := s.dependencies.parseConfig()
	if err != nil {
		return ctxerrors.Wrap(err, "load HTTP service configuration")
	}

	upstreams, err := config.Upstreams()
	if err != nil {
		return ctxerrors.Wrap(err, "load configured providers")
	}

	logValidatedConfig(ctx, config, upstreams)

	if err := enterWorkingDirectory(config.WorkingDirectory); err != nil {
		return err
	}

	metricRegistry := metrics.New()

	server, assembled, err := s.newAPIServer(
		ctx,
		config,
		upstreams,
		metricRegistry,
	)
	if err != nil {
		return err
	}

	defer func() {
		runErr = errors.Join(runErr, closeState(ctx, assembled.Handle))
	}()

	runtime := assembled.Runtime

	metricsServer, err := metrics.NewServer(metricRegistry)
	if err != nil {
		return ctxerrors.Wrap(err, "create metrics server")
	}

	metricsListener, err := s.dependencies.listen(
		ctx,
		networkTCP,
		config.MetricsListenAddress,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "open metrics listener")
	}

	logger.Info(
		"HTTP service initialized",
		"listen_address", config.HTTPListenAddress,
		"metrics_listen_address", config.MetricsListenAddress,
		"provider_count", len(upstreams),
		"default_model", config.DefaultModel,
	)

	defer func() {
		runErr = errors.Join(runErr, stopSessionJobs(ctx, runtime))
	}()

	if err := serveBoth(
		ctx,
		server,
		metricsServer,
		metricsListener,
	); err != nil {
		return err
	}

	return nil
}

func serveBoth(
	ctx context.Context,
	apiServer *peenhttp.Server,
	metricsServer *metrics.Server,
	metricsListener net.Listener,
) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serveErrors := make(chan error, serveWorkerCount)
	go func() {
		serveErrors <- apiServer.Serve(serveCtx)
	}()
	go func() {
		serveErrors <- metricsServer.ServeListener(serveCtx, metricsListener)
	}()

	firstErr := <-serveErrors

	cancel()

	secondErr := <-serveErrors

	return errors.Join(firstErr, secondErr)
}

// logValidatedConfig records the validated startup configuration shape with
// every secret redacted: never PEEN_API_TOKEN, and never a provider API key
// resolved through an upstream's apiKeyEnv. Only counts, names, bounds, and
// booleans are safe to print here.
//
//nolint:funlen // One record lists every safe config field.
func logValidatedConfig(
	ctx context.Context,
	config peenconfig.Config,
	upstreams []peenconfig.Upstream,
) {
	upstreamNames := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstreamNames = append(upstreamNames, upstream.Name)
	}

	ctxscope.GetLogger(ctx).Info(
		"validated configuration",
		"config_directory", config.ConfigDirectory,
		"working_directory", config.WorkingDirectory,
		"agent", config.Agent,
		"default_model", config.DefaultModel,
		"compaction_model", config.CompactionModel,
		"compaction_mode", config.CompactionMode,
		"max_context_tokens", config.MaxContextTokens,
		"compaction_output_tokens", config.CompactionOutputTokens,
		"compaction_timeout", config.CompactionTimeout,
		"turn_timeout", config.TurnTimeout,
		"max_concurrent_turns", config.MaxConcurrentTurns,
		"max_queued_user_messages", config.MaxQueuedUserMessages,
		"max_message_bytes", config.MaxMessageBytes,
		"max_system_prompt_bytes", config.MaxSystemPromptBytes,
		"max_stored_message_bytes", config.MaxStoredMessageBytes,
		"max_tool_rounds", config.MaxToolRounds,
		"max_concurrent_tools", config.MaxConcurrentTools,
		"tool_timeout", config.ToolTimeout,
		"max_tool_result_tokens", config.MaxToolResultTokens,
		"enable_workspace_hooks", config.EnableWorkspaceHooks,
		"hook_command_timeout", config.HookCommandTimeout,
		"max_hook_command_output", config.MaxHookCommandOutput,
		"tool_max_list_entries", config.ToolMaxListEntries,
		"tool_max_list_depth", config.ToolMaxListDepth,
		"tool_max_search_matches", config.ToolMaxSearchMatches,
		"tool_max_search_file_bytes", config.ToolMaxSearchFileBytes,
		"tool_max_read_bytes", config.ToolMaxReadBytes,
		"tool_max_read_lines", config.ToolMaxReadLines,
		"tool_max_write_bytes", config.ToolMaxWriteBytes,
		"tool_max_edits", config.ToolMaxEdits,
		"tool_max_diff_bytes", config.ToolMaxDiffBytes,
		"tool_max_remove_entries", config.ToolMaxRemoveEntries,
		"tool_max_command_output_bytes", config.ToolMaxCommandOutputBytes,
		"tool_command_timeout", config.ToolCommandTimeout,
		"tool_max_command_timeout", config.ToolMaxCommandTimeout,
		"max_pending_events", config.MaxPendingEvents,
		"max_event_summary_bytes", config.MaxEventSummaryBytes,
		"max_event_data_bytes", config.MaxEventDataBytes,
		"max_event_wakes_per_hour", config.MaxEventWakesPerHour,
		"max_child_agent_depth", config.MaxChildAgentDepth,
		"max_child_agent_turns", config.MaxChildAgentTurns,
		"max_concurrent_agent_runs", config.MaxConcurrentAgentRunsPerSession,
		"max_agent_run_event_count", config.MaxAgentRunEventCount,
		"max_agent_run_event_bytes", config.MaxAgentRunEventBytes,
		"max_adhoc_agent_instruction_bytes",
		config.MaxAdHocAgentInstructionBytes,
		"http_listen_address", config.HTTPListenAddress,
		"metrics_listen_address", config.MetricsListenAddress,
		"api_token_configured", config.APIToken != "",
		"provider_count", len(upstreams),
		"provider_names", upstreamNames,
	)
}

// enterWorkingDirectory makes PEEN_WORKING_DIR the process directory before
// anything resolves a path against it.
//
// Without this the setting is only a default workspace string, so a relative
// path anywhere else, a tool's own relative resolution or a spawned command's
// inherited directory, still lands wherever the process happened to start.
func enterWorkingDirectory(workingDirectory string) error {
	if workingDirectory == "" {
		return nil
	}

	if err := os.Chdir(workingDirectory); err != nil {
		return ctxerrors.Wrap(err, "enter configured working directory")
	}

	return nil
}

// closeState closes the durable store, reporting rather than hiding a failure
// so a corrupted shutdown does not look clean.
func closeState(ctx context.Context, handle *db.Handle) error {
	if err := handle.Close(); err != nil {
		wrapped := ctxerrors.Wrap(err, "close HTTP service state")
		ctxscope.GetLogger(ctx).Error(
			"HTTP service state close failed",
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

func (s *HTTPServer) newAPIServer(
	ctx context.Context,
	config peenconfig.Config,
	upstreams []peenconfig.Upstream,
	metricRegistry *metrics.Metrics,
) (*peenhttp.Server, *agent.Assembled, error) {
	// Discovery stays here because it is the one part that genuinely differs
	// between the service and an embedding Go program: a deployment discovers
	// models from its configured providers, an embedder supplies its own.
	registry, err := agent.NewRegistry(ctx, agent.RegistryOptions{
		Upstreams:        upstreams,
		DefaultModel:     config.DefaultModel,
		CompactionModel:  config.CompactionModel,
		MaxContextTokens: config.MaxContextTokens,
		Factory:          s.dependencies.driverFactory,
	})
	if err != nil {
		return nil, nil, ctxerrors.Wrap(
			err,
			"discover configured provider models",
		)
	}

	assembled, err := agent.Assemble(ctx, agent.AssembleOptions{
		Runtime:       runtimeOptions(config, registry, metricRegistry),
		HarnessLimits: harnessLimits(),
		StoreOptions: session.Options{
			MaxStoredMessageBytes: config.MaxStoredMessageBytes,
		},
	})
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "assemble agent runtime")
	}

	server, err := peenhttp.New(peenhttp.Dependencies{
		Runtime:        assembled.Runtime,
		APIToken:       config.APIToken,
		ListenAddress:  config.HTTPListenAddress,
		Metrics:        metricRegistry,
		ServiceContext: func() context.Context { return ctx },
	})
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "create HTTP API server")
	}

	return server, assembled, nil
}

// runtimeOptions maps the deployment's configuration onto the runtime. Store
// and Resolver are left zero: Assemble fills them from the same configuration
// directory, so neither caller can wire one and forget the other.
func runtimeOptions(
	config peenconfig.Config,
	registry agent.ModelResolver,
	metricRegistry *metrics.Metrics,
) agent.RuntimeOptions {
	return agent.RuntimeOptions{
		Models:           registry,
		RootAgent:        config.Agent,
		DefaultModel:     config.DefaultModel,
		DefaultWorkspace: config.WorkingDirectory,
		MaxContextTokens: config.MaxContextTokens,
		TurnTimeout:      config.TurnTimeout,

		MaxSystemPromptBytes:  config.MaxSystemPromptBytes,
		MaxMessageBytes:       config.MaxMessageBytes,
		MaxConcurrentTurns:    config.MaxConcurrentTurns,
		MaxQueuedUserMessages: config.MaxQueuedUserMessages,

		CompactionMode:         config.CompactionMode,
		CompactionModel:        config.CompactionModel,
		CompactionOutputTokens: config.CompactionOutputTokens,
		CompactionTimeout:      config.CompactionTimeout,

		Events: events.NewBus(
			eventBusOptions(config, metricRegistry),
		),
		Metrics:              metricRegistry,
		MaxEventWakesPerHour: config.MaxEventWakesPerHour,
		ConfigDirectory:      config.ConfigDirectory,
		AgentLimits:          agentRunLimits(config),
		ToolLimits:           toolLimits(config),
		MaxToolRounds:        config.MaxToolRounds,
		MaxConcurrentTools:   config.MaxConcurrentTools,
		ToolTimeout:          config.ToolTimeout,
		MaxToolResultTokens:  config.MaxToolResultTokens,
		EnableWorkspaceHooks: config.EnableWorkspaceHooks,
		HookCommandTimeout:   config.HookCommandTimeout,
		MaxHookCommandOutput: config.MaxHookCommandOutput,
	}
}

// eventBusOptions maps the deployment's event bounds onto the event package.
func eventBusOptions(
	config peenconfig.Config,
	metricRegistry *metrics.Metrics,
) events.Options {
	return events.Options{
		MaxPendingPerSession: config.MaxPendingEvents,
		MaxSummaryBytes:      config.MaxEventSummaryBytes,
		MaxDataBytes:         config.MaxEventDataBytes,
		Metrics:              metricRegistry,
	}
}

// agentRunLimits maps the deployment's child-agent bounds onto the runtime.
// Without this the env vars parse and validate but never reach launch_agent,
// so an operator would set them and see nothing change.
func agentRunLimits(config peenconfig.Config) agent.AgentRunLimits {
	return agent.AgentRunLimits{
		MaxDepth:                 config.MaxChildAgentDepth,
		MaxChildTurns:            config.MaxChildAgentTurns,
		MaxConcurrentRuns:        config.MaxConcurrentAgentRunsPerSession,
		MaxEventCount:            config.MaxAgentRunEventCount,
		MaxEventBytes:            config.MaxAgentRunEventBytes,
		MaxAdHocInstructionBytes: adHocInstructionBytes(config),
	}
}

// adHocInstructionBytes keeps an inline agent definition inside the bound a
// stored agent file already has.
//
// The two settings come from different places, the deployment's own env var
// and the harness file bound, so equal defaults are not enough: raising one
// alone would let an inline definition carry instructions the same deployment
// would refuse to read from disk. Taking the smaller of the two makes the
// contract hold no matter which one moves.
func adHocInstructionBytes(config peenconfig.Config) int {
	storedFileBytes := harnessLimits().MaxFileBytes
	if int64(config.MaxAdHocAgentInstructionBytes) > storedFileBytes {
		return int(storedFileBytes)
	}

	return config.MaxAdHocAgentInstructionBytes
}

// harnessLimits is the single place the harness bounds are decided, so the
// resolver and anything derived from them cannot disagree.
func harnessLimits() harness.Limits {
	return harness.DefaultLimits()
}

// toolLimits maps the deployment's tool bounds onto the tool package. These
// are resource and context bounds, never permission gates.
func toolLimits(config peenconfig.Config) tools.Limits {
	return tools.Limits{
		MaxListEntries:        config.ToolMaxListEntries,
		MaxListDepth:          config.ToolMaxListDepth,
		MaxSearchMatches:      config.ToolMaxSearchMatches,
		MaxSearchFileBytes:    config.ToolMaxSearchFileBytes,
		MaxReadBytes:          config.ToolMaxReadBytes,
		MaxReadLines:          config.ToolMaxReadLines,
		MaxWriteBytes:         config.ToolMaxWriteBytes,
		MaxEdits:              config.ToolMaxEdits,
		MaxDiffBytes:          config.ToolMaxDiffBytes,
		MaxRemoveEntries:      config.ToolMaxRemoveEntries,
		MaxCommandOutputBytes: config.ToolMaxCommandOutputBytes,
		CommandTimeout:        config.ToolCommandTimeout,
		MaxCommandTimeout:     config.ToolMaxCommandTimeout,
	}
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	serviceCtx := ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))

	ctxscope.GetLogger(serviceCtx).Info("stopping HTTP service")

	return nil
}
