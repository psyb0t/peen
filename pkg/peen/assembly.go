package peen

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	internalconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

func resolveEmbeddingWorkspace(options Options) (string, bool, error) {
	workspace := options.Workspace

	virtual := options.Filesystem != nil
	if host, ok := options.Filesystem.(*HostFS); ok {
		virtual = false

		if workspace == "" {
			workspace = host.directory
		}
	}

	if virtual {
		if options.WorkspaceID == "" {
			return "", false, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
				"virtual workspace ID")
		}

		workspace = options.WorkspaceID
	}

	var err error
	if workspace == "" {
		workspace, err = resolveStartupWorkspace()
	}

	if err != nil {
		return "", false, ctxerrors.Wrap(err, "resolve startup workspace")
	}

	return workspace, virtual, nil
}

func resolveEmbeddingDirectories(options Options) (string, string, error) {
	configDirectory, err := filepath.Abs(options.ConfigDirectory)
	if err != nil {
		return "", "", ctxerrors.Wrap(err, "resolve config directory")
	}

	stateDirectory := options.StateDirectory
	if stateDirectory == "" {
		stateDirectory = configDirectory + stateDirectorySuffix
	}

	stateDirectory, err = filepath.Abs(stateDirectory)
	if err != nil {
		return "", "", ctxerrors.Wrap(err, "resolve state directory")
	}

	if err := internalconfig.ValidateStateDirectories(
		configDirectory, stateDirectory,
	); err != nil {
		return "", "", ctxerrors.Wrap(err, "separate embedded state")
	}

	return configDirectory, stateDirectory, nil
}

//nolint:ireturn // Assembly accepts static and dynamic model resolvers.
func resolveEmbeddingModels(options Options) (agent.ModelResolver, error) {
	var (
		err    error
		models agent.ModelResolver
	)
	if options.ModelResolver != nil {
		models = modelResolverAdapter{resolver: options.ModelResolver}
	} else {
		models, err = staticRegistry(options.Models)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "create model registry")
		}
	}

	return models, nil
}

type embeddingConfiguration struct {
	runtime        agent.RuntimeOptions
	stateDirectory string
	temporaryState string
	telemetry      *telemetry
}

func configureEmbedding(
	options Options,
	configDirectory, workspace, stateDirectory string,
	virtual bool,
	models agent.ModelResolver,
	extraTools []elelem.Tool,
	callback func(context.Context, hooks.Invocation) (hooks.Outcome, error),
	hub *eventHub,
) (*embeddingConfiguration, error) {
	temporaryState := ""

	var err error
	if options.InMemoryStore {
		temporaryState, err = os.MkdirTemp("", temporaryStatePrefix)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "create temporary hook state")
		}

		stateDirectory = temporaryState
	}

	telemetry, err := newTelemetry(options, stateDirectory)
	if err != nil {
		return nil, errors.Join(err, removeTemporaryState(temporaryState))
	}

	runtimeConfig := runtimeOptions(configDirectory, workspace, models, options)
	runtimeConfig.WorkflowManaged = true
	runtimeConfig.Logger = telemetry.logger
	runtimeConfig.Metrics = telemetry.metrics
	runtimeConfig.Filesystem = options.Filesystem
	runtimeConfig.VirtualFilesystem = virtual
	runtimeConfig.DisableProcessTools = virtual && options.CommandRunner == nil

	runtimeConfig.CommandRunner = options.CommandRunner
	if options.CommandRunner != nil {
		runtimeConfig.HookCommandRunner = hooks.WithCommander(
			options.CommandRunner,
		)
	} else if virtual {
		runtimeConfig.HookCommandRunner = denyVirtualCommand
	}

	runtimeConfig.Tools = extraTools
	runtimeConfig.HookCallback = callback
	runtimeConfig.DisableTools = options.DisableTools

	runtimeConfig.ObserveEvent = hub.publish
	runtimeConfig.HookStateRoot = hooks.DefaultStateRoot(stateDirectory)

	return &embeddingConfiguration{
		runtime:        runtimeConfig,
		stateDirectory: stateDirectory,
		temporaryState: temporaryState,
		telemetry:      telemetry,
	}, nil
}

func embeddingAssembly(
	options Options,
	configuration *embeddingConfiguration,
	readers []workspacefs.Reader,
) agent.AssembleOptions {
	return agent.AssembleOptions{
		StateDirectory: configuration.stateDirectory,
		InMemoryStore:  options.InMemoryStore,
		Runtime:        configuration.runtime,
		HarnessLimits:  harness.Limits(options.HarnessLimits),
		HarnessOptions: []harness.ResolverOption{
			harness.WithMemoryLayers(readers),
			harness.WithWorkspaceFilesystem(
				options.Filesystem,
				configuration.runtime.VirtualFilesystem,
			),
			harness.WithDisabledCoreSkills(options.DisabledCoreSkills),
			harness.WithDisabledCoreAgents(options.DisabledCoreAgents),
		},
		StoreOptions: session.Options{
			Clock:                 options.Clock,
			NewID:                 options.NewID,
			MaxStoredMessageBytes: options.MaxStoredMessageBytes,
		},
	}
}

type embeddingExtensions struct {
	tools    []elelem.Tool
	callback hooks.Callback
	readers  []workspacefs.Reader
}

func prepareExtensions(options Options) (embeddingExtensions, error) {
	tools, err := customTools(options.Tools)
	if err != nil {
		return embeddingExtensions{}, err
	}

	callback, err := hookCallback(options.Hooks)
	if err != nil {
		return embeddingExtensions{}, err
	}

	readers, err := memoryHarness(options.Harness)
	if err != nil {
		return embeddingExtensions{}, err
	}

	return embeddingExtensions{
		tools:    tools,
		callback: callback,
		readers:  readers,
	}, nil
}
