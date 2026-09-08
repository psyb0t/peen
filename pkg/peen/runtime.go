package peen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	internalconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// defaultMaxContextTokens mirrors PEEN_MAX_CONTEXT_TOKENS's deployment
	// default, so an embedding caller that leaves it unset behaves like the
	// HTTP service's own default deployment.
	defaultMaxContextTokens = 32768
	// defaultTurnTimeout mirrors PEEN_TURN_TIMEOUT's deployment default.
	defaultTurnTimeout = 10 * time.Minute
)

// Runtime is Peen's embedding surface over the internal agent runtime.
//
// The public package sketch calls for a type alias to the internal runtime,
// but agent.Runtime's exported method set includes SendMessage, StreamMessage,
// ListSessionJobs, CancelSessionAgentRun, ShutdownJobs, and more: aliasing it
// would expose all of that, the opposite of the deliberately small surface
// this package promises. Wrapping the internal runtime behind an unexported
// field is how that narrow surface is actually delivered.
type Runtime struct {
	internal *agent.Runtime
}

// New builds one Runtime from explicit, validated dependencies.
func New(options Options) (*Runtime, error) {
	if err := options.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate peen options")
	}

	options = options.withDefaults()

	workspace, err := resolveDefaultWorkspace(options.DefaultWorkspace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve default workspace")
	}

	configDirectory, err := filepath.Abs(options.ConfigDirectory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve config directory")
	}

	models, err := staticRegistry(options.Models)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create model registry")
	}

	// One assembly, shared with the HTTP service. Anything that must be true
	// of every runtime, such as settling turns a previous process left
	// running, happens there so an embedding program cannot end up with a
	// differently wired runtime than a deployment gets.
	assembled, err := agent.Assemble(
		context.Background(),
		agent.AssembleOptions{
			Runtime: runtimeOptions(
				configDirectory,
				workspace,
				models,
				options,
			),
			StoreOptions: session.Options{
				Clock:                 options.Clock,
				NewID:                 options.NewID,
				MaxStoredMessageBytes: options.MaxStoredMessageBytes,
			},
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "assemble peen runtime")
	}

	return &Runtime{internal: assembled.Runtime}, nil
}

// runtimeOptions converts the public Options plus the dependencies already
// built from it into the internal runtime's construction options.
// runtimeOptions maps the public Options onto the internal runtime's
// construction options. Store and Resolver are left zero: Assemble fills them
// from the same configuration directory, so this package and the HTTP service
// cannot wire one and forget the other.
func runtimeOptions(
	configDirectory string,
	workspace string,
	models *agent.Registry,
	options Options,
) agent.RuntimeOptions {
	return agent.RuntimeOptions{
		Models:           models,
		RootAgent:        options.RootAgent,
		DefaultModel:     options.DefaultModel,
		DefaultWorkspace: workspace,
		MaxContextTokens: options.MaxContextTokens,
		TurnTimeout:      options.TurnTimeout,

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
		EnableWorkspaceHooks: options.EnableWorkspaceHooks,
		HookCommandTimeout:   options.HookCommandTimeout,
		MaxHookCommandOutput: options.MaxHookCommandOutput,

		MaxEventWakesPerHour: options.MaxEventWakesPerHour,
		Events:               events.NewBus(events.Options{}),
		ConfigDirectory:      configDirectory,
	}
}

// resolveDefaultWorkspace captures the caller's current working directory
// when none was supplied.
func resolveDefaultWorkspace(workspace string) (string, error) {
	if workspace != "" {
		return workspace, nil
	}

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
	input, err := toTurnRequest(request)
	if err != nil {
		return MessageResult{}, ctxerrors.Wrap(err, "convert message request")
	}

	result, err := r.internal.Run(ctx, input)
	if err != nil {
		return MessageResult{}, translateTurnError(err)
	}

	return MessageResult{
		SessionID: result.SessionID.String(),
		Message:   result.Text,
		Queued:    result.Queued,
	}, nil
}

// Stream runs one durable turn, delivering each event to sink as Peen
// produces it, then returns the same final answer Message would. It shares
// the same execution pipeline as Message; only event delivery differs.
func (r *Runtime) Stream(
	ctx context.Context,
	request MessageRequest,
	sink EventSink,
) (MessageResult, error) {
	input, err := toTurnRequest(request)
	if err != nil {
		return MessageResult{}, ctxerrors.Wrap(err, "convert message request")
	}

	if sink != nil {
		input.OnEvent = func(event agent.Event) error {
			return sink(Event{Type: event.Type, Payload: event.Payload})
		}
	}

	result, err := r.internal.Run(ctx, input)
	if err != nil {
		return MessageResult{}, translateTurnError(err)
	}

	return MessageResult{
		SessionID: result.SessionID.String(),
		Message:   result.Text,
		Queued:    result.Queued,
	}, nil
}

// ListMessages reads one bounded page of an existing session's transcript.
func (r *Runtime) ListMessages(
	ctx context.Context,
	request ListMessagesRequest,
) (ListMessagesResult, error) {
	params, err := toListMessagesParams(request)
	if err != nil {
		return ListMessagesResult{}, err
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

// Session reads one existing session's read-only metadata.
func (r *Runtime) Session(
	ctx context.Context,
	sessionID string,
) (SessionDetails, error) {
	parsed, err := parseSessionID(sessionID)
	if err != nil {
		return SessionDetails{}, err
	}

	stored, err := r.internal.Session(ctx, parsed)
	if err != nil {
		return SessionDetails{}, ctxerrors.Wrap(err, "get session")
	}

	return toSessionDetails(*stored), nil
}

// Cancel requests cancellation of an existing session's active turn. It is
// idempotent: cancelling a session with no active turn reports
// CancelRequested false rather than failing.
func (r *Runtime) Cancel(
	ctx context.Context,
	sessionID string,
) (CancelResult, error) {
	parsed, err := parseSessionID(sessionID)
	if err != nil {
		return CancelResult{}, err
	}

	response, err := r.internal.CancelSession(ctx, parsed)
	if err != nil {
		return CancelResult{}, ctxerrors.Wrap(err, "cancel session")
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

// parseSessionID validates a session ID crossing the public boundary as a
// string.
func parseSessionID(sessionID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(sessionID))
	if err != nil {
		return uuid.Nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"session id %q is not a valid UUID",
			sessionID,
		)
	}

	return parsed, nil
}
