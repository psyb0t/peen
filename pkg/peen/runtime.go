package peen

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	internalconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	// defaultMaxContextTokens mirrors PEEN_MAX_CONTEXT_TOKENS's deployment
	// default, so an embedding caller that leaves it unset behaves like the
	// HTTP service's own default deployment.
	defaultMaxContextTokens     = 32768
	defaultMaxEventWakesPerHour = 60
	// defaultTurnTimeout mirrors PEEN_TURN_TIMEOUT's deployment default.
	defaultTurnTimeout   = 10 * time.Minute
	stateDirectorySuffix = "-state"
	temporaryStatePrefix = "peen-state-"
)

// Runtime owns storage and execution for one or more durable sessions.
// Call Close after the last operation to release its resources.
// Runtime values must not be copied after construction.
type Runtime struct {
	internal       *agent.Runtime
	handle         *db.Handle
	closeGate      chan struct{}
	closed         atomic.Bool
	store          *session.Store
	options        Options
	temporaryState string
	eventHub       *eventHub
	telemetry      *telemetry
}

// New builds one Runtime from explicit, validated dependencies.
func New(options Options) (*Runtime, error) {
	if err := options.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate peen options")
	}

	options = options.withDefaults()

	workspace, virtual, err := resolveEmbeddingWorkspace(options)
	if err != nil {
		return nil, err
	}

	configDirectory, stateDirectory, err := resolveEmbeddingDirectories(options)
	if err != nil {
		return nil, err
	}

	models, err := resolveEmbeddingModels(options)
	if err != nil {
		return nil, err
	}

	extensions, err := prepareExtensions(options)
	if err != nil {
		return nil, err
	}

	hub := newEventHub(options.EventLimits.SubscriberBuffer)

	configuration, err := configureEmbedding(
		options, configDirectory, workspace, stateDirectory, virtual,
		models, extensions.tools, extensions.callback, hub,
	)
	if err != nil {
		return nil, err
	}

	// One assembly, shared with the HTTP service. Anything that must be true
	// of every runtime, such as settling turns a previous process left
	// running, happens there so an embedding program cannot end up with a
	// differently wired runtime than a deployment gets.
	assembled, err := agent.Assemble(
		context.Background(),
		embeddingAssembly(options, configuration, extensions.readers),
	)
	if err != nil {
		return nil, errors.Join(
			ctxerrors.Wrap(err, "assemble peen runtime"),
			removeTemporaryState(configuration.temporaryState),
			configuration.telemetry.close(),
		)
	}

	return &Runtime{
		internal:       assembled.Runtime,
		closeGate:      make(chan struct{}, 1),
		handle:         assembled.Handle,
		store:          assembled.Store,
		options:        options,
		temporaryState: configuration.temporaryState,
		eventHub:       hub,
		telemetry:      configuration.telemetry,
	}, nil
}

// Close drains work and releases SQLite. Once closing starts, new calls fail
// with ErrClosed. If ctx expires, admitted work is cancelled; call Close again
// to finish cleanup after it ends. Repeated successful closes are harmless.
func (r *Runtime) Close(ctx context.Context) error {
	if r.closed.Load() {
		return nil
	}

	select {
	case r.closeGate <- struct{}{}:
		defer func() { <-r.closeGate }()
	case <-ctx.Done():
		if err := r.internal.Shutdown(ctx); err != nil {
			return ctxerrors.Wrap(err, "cancel closing runtime")
		}

		return nil
	}

	if r.closed.Load() {
		return nil
	}

	if err := r.internal.Shutdown(ctx); err != nil {
		return ctxerrors.Wrap(err, "shut down embedded runtime")
	}

	if err := r.handle.Close(); err != nil {
		return ctxerrors.Wrap(err, "close embedded state")
	}

	if err := removeTemporaryState(r.temporaryState); err != nil {
		return err
	}

	if err := r.telemetry.close(); err != nil {
		return err
	}

	r.eventHub.close()
	r.closed.Store(true)

	return nil
}

func removeTemporaryState(path string) error {
	if path == "" {
		return nil
	}

	if err := os.RemoveAll(path); err != nil {
		return ctxerrors.Wrap(err, "remove temporary hook state")
	}

	return nil
}

// runtimeOptions maps embedding values to the shared assembly pipeline.
func runtimeOptions(
	configDirectory string,
	workspace string,
	models agent.ModelResolver,
	options Options,
) agent.RuntimeOptions {
	return agent.RuntimeOptions{
		Models:             models,
		RootAgent:          options.RootAgent,
		DefaultModel:       options.DefaultModel,
		DefaultWorkspace:   workspace,
		MaxContextTokens:   options.MaxContextTokens,
		TurnTimeout:        options.TurnTimeout,
		BaseSystemPrompt:   options.BaseSystemPrompt,
		AppendSystemPrompt: options.AppendSystemPrompt,
		AskTimeout:         options.AskTimeout,
		ToolLimits:         tools.Limits(options.ToolLimits),
		AgentLimits:        agent.AgentRunLimits(options.AgentLimits),

		MaxSystemPromptBytes:  options.MaxSystemPromptBytes,
		MaxMessageBytes:       options.MaxMessageBytes,
		MaxConcurrentTurns:    options.MaxConcurrentTurns,
		MaxQueuedUserMessages: options.MaxQueuedUserMessages,

		CompactionMode: internalconfig.CompactionMode(
			options.CompactionMode,
		),
		CompactionModel:        options.CompactionModel,
		CompactionPrompt:       options.CompactionPrompt,
		CompactionOutputTokens: options.CompactionOutputTokens,
		CompactionTimeout:      options.CompactionTimeout,

		MaxToolRounds:        options.MaxToolRounds,
		MaxConcurrentTools:   options.MaxConcurrentTools,
		ToolTimeout:          options.ToolTimeout,
		MaxToolResultTokens:  options.MaxToolResultTokens,
		HookCommandTimeout:   options.HookCommandTimeout,
		MaxHookCommandOutput: options.MaxHookCommandOutput,

		MaxEventWakesPerHour: options.MaxEventWakesPerHour,
		Events: events.NewBus(events.Options{
			MaxPendingPerSession: options.EventLimits.MaxPendingPerSession,
			MaxSummaryBytes:      options.EventLimits.MaxSummaryBytes,
			MaxDataBytes:         options.EventLimits.MaxDataBytes,
			SubscriberBuffer:     options.EventLimits.SubscriberBuffer,
		}),
		ConfigDirectory: configDirectory,
	}
}

// resolveStartupWorkspace captures the process working directory once while
// the embedded runtime starts.
func resolveStartupWorkspace() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get current working directory")
	}

	return current, nil
}

// Message runs one durable turn to completion and returns its final answer.
func (r *Runtime) Message(
	ctx context.Context,
	request MessageRequest,
) (MessageResult, error) {
	return r.Session(r.internal.SessionID().String()).Send(ctx, request, nil)
}

// Stream runs one durable turn, delivering each event to sink as Peen
// produces it, then returns the same final answer Message would. It shares
// the same execution pipeline as Message; only event delivery differs.
func (r *Runtime) Stream(
	ctx context.Context,
	request MessageRequest,
	sink EventSink,
) (MessageResult, error) {
	return r.Session(r.internal.SessionID().String()).Send(ctx, request, sink)
}

// ListMessages reads one bounded page of this workspace's transcript.
func (r *Runtime) ListMessages(
	ctx context.Context,
	request ListMessagesRequest,
) (ListMessagesResult, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return ListMessagesResult{}, ctxerrors.Wrap(
			err,
			"read initial messages",
		)
	}
	defer finish()

	params, err := toListMessagesParams(request, r.internal.SessionID())
	if err != nil {
		return ListMessagesResult{}, ctxerrors.Wrap(
			err,
			"read initial messages",
		)
	}

	page, err := r.internal.ListMessages(ctx, params)
	if err != nil {
		return ListMessagesResult{}, ctxerrors.Wrap(
			err,
			"list session messages",
		)
	}

	result, err := toListMessagesResult(*page)
	if err != nil {
		return ListMessagesResult{}, ctxerrors.Wrap(err, "convert message page")
	}

	return result, nil
}

// Details reads this workspace's read-only metadata.
func (r *Runtime) Details(ctx context.Context) (SessionDetails, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return SessionDetails{}, ctxerrors.Wrap(err, "read initial session")
	}
	defer finish()

	stored, err := r.internal.Session(ctx, r.internal.SessionID())
	if err != nil {
		return SessionDetails{}, ctxerrors.Wrap(err, "get workspace details")
	}

	return toSessionDetails(*stored), nil
}

// Cancel requests cancellation of this workspace's active turn and every
// child agent still running in it. It is idempotent: with no active turn and
// no running child it reports CancelRequested false rather than failing.
func (r *Runtime) Cancel(ctx context.Context) (CancelResult, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return CancelResult{}, ctxerrors.Wrap(err, "cancel initial session")
	}
	defer finish()

	response, err := r.internal.CancelSession(ctx, r.internal.SessionID())
	if err != nil {
		return CancelResult{}, ctxerrors.Wrap(err, "cancel the active turn")
	}

	return CancelResult{CancelRequested: response.CancelRequested}, nil
}

// translateTurnError maps the internal runtime's transport-neutral turn
// failures onto the same commerr sentinels the HTTP surface returns, so an
// embedding caller sees one consistent error vocabulary regardless of
// transport.
func translateTurnError(err error) error {
	if errors.Is(err, session.ErrSessionBusy) {
		return ctxerrors.Wrap(
			commerr.ErrConflict,
			"session already has an active turn",
		)
	}

	if errors.Is(err, context.Canceled) {
		return ctxerrors.Wrap(commerr.ErrCancelled, "turn was cancelled")
	}

	return err
}
