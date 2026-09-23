// Package run is the body of the internal `peen worker` command.
//
// One worker process serves one session. It reads the launch document its
// controller handed it, registers over the private socket, builds a session
// runtime whose durable surface is that socket, and then serves the
// controller's commands until it is told to stop.
//
// It never opens the controller's SQLite database. Every durable read and
// write is a protocol call, so the control plane stays the sole owner of
// durable state.
package run

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

// maxLaunchDocumentBytes bounds the launch document read from stdin. The
// document is a handful of identifiers and paths, so anything larger is a
// mistake or an attempt to exhaust this process before it starts.
const maxLaunchDocumentBytes = 64 << 10

// Options are the seams a worker run takes. Every field has a production
// default, so the command itself passes none.
type Options struct {
	// LaunchInput carries the launch document. Nil reads os.Stdin, which is
	// how a native worker receives it without the credential ever appearing in
	// the process table or on disk.
	LaunchInput io.Reader

	// ParseConfig loads the deployment configuration. Nil uses the real
	// environment through peenconfig.ParseWorker, which does not require the
	// controller's state directory.
	ParseConfig func() (peenconfig.Config, error)

	// Driver overrides provider construction. Nil discovers the configured
	// providers.
	Driver agent.DriverFactory
}

// Run executes one worker to completion.
//
// It returns when the controller asks the worker to shut down, when the
// connection ends, or when the context is cancelled. A registration the
// controller refuses is a terminal failure: the launch this process was given
// is one the controller will never accept, so retrying would loop.
func Run(ctx context.Context, options Options) error {
	document, err := readLaunchDocument(options.LaunchInput)
	if err != nil {
		return err
	}

	ctx = ctxscope.Set(
		ctx,
		ctxscope.Attr("session_id", document.SessionID.String()),
		ctxscope.Attr("worker_generation_id", document.GenerationID.String()),
		ctxscope.Attr("execution_profile", document.Profile),
	)

	parseConfig := options.ParseConfig
	if parseConfig == nil {
		// ParseWorker rather than Parse: a worker owns no durable state, so it
		// neither requires nor reads PEEN_STATE_DIR. The controller keeps that
		// path out of a Docker worker's environment on purpose.
		parseConfig = peenconfig.ParseWorker
	}

	deployment, err := parseConfig()
	if err != nil {
		return ctxerrors.Wrap(err, "load worker configuration")
	}

	// The controller decides where the worker reads its configuration from,
	// so the launch document wins over anything the environment says.
	deployment.ConfigDirectory = document.ConfigDirectory

	handler := newCommandHandler()

	connection, err := protocol.Dial(ctx, document, handler)
	if err != nil {
		return ctxerrors.Wrap(err, "connect to the controller")
	}

	defer connection.Close()

	serveCtx, stop := context.WithCancel(ctx)
	defer stop()

	// The read loop has to run before the runtime is built. Building it
	// attaches the worker to the session its controller already created, and
	// that is a durable protocol call whose answer arrives only through this
	// loop. A command that lands first is refused, because the handler has no
	// runtime yet.
	served := make(chan error, 1)

	go func() { served <- connection.Serve(serveCtx) }()

	runtime, err := buildRuntime(
		ctx,
		deployment,
		document,
		connection,
		options.Driver,
	)
	if err != nil {
		return err
	}

	handler.attach(runtime, connection.Store())

	ctxscope.GetLogger(ctx).Info(
		"session worker ready",
		"workspace", connection.Ack.Workspace,
	)

	return serve(ctx, stop, handler, served)
}

// serve waits for the controller to stop the worker or for the connection to
// end, then reports how the read loop finished.
func serve(
	ctx context.Context,
	stop context.CancelFunc,
	handler *commandHandler,
	served <-chan error,
) error {
	select {
	case err := <-served:
		if err != nil {
			return ctxerrors.Wrap(err, "serve the controller protocol")
		}

		return nil
	case <-handler.stopped:
	case <-ctx.Done():
	}

	stop()

	if err := <-served; err != nil {
		return ctxerrors.Wrap(err, "serve the controller protocol")
	}

	return nil
}

// readLaunchDocument takes the one launch document this worker was given.
//
// It is read from a pipe rather than an argument or an environment variable so
// the credential inside it never reaches the process table. The read is bounded
// and the decoded document is validated, because `peen worker` is not a general
// user command: without a complete launch it has no session, no endpoint, and
// nothing to do.
func readLaunchDocument(input io.Reader) (worker.LaunchDocument, error) {
	if input == nil {
		input = os.Stdin
	}

	raw, err := io.ReadAll(io.LimitReader(input, maxLaunchDocumentBytes+1))
	if err != nil {
		return worker.LaunchDocument{}, ctxerrors.Wrap(
			err,
			"read the worker launch document",
		)
	}

	if len(raw) > maxLaunchDocumentBytes {
		return worker.LaunchDocument{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"the worker launch document exceeds %d bytes",
			maxLaunchDocumentBytes,
		)
	}

	if len(raw) == 0 {
		return worker.LaunchDocument{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"peen worker requires a controller-issued launch document",
		)
	}

	document, err := worker.DecodeLaunchDocument(raw)
	if err != nil {
		return worker.LaunchDocument{}, ctxerrors.Wrap(
			err,
			"decode the worker launch document",
		)
	}

	return document, nil
}

// buildRuntime wires this worker's session runtime over the protocol store.
func buildRuntime(
	ctx context.Context,
	deployment peenconfig.Config,
	document worker.LaunchDocument,
	connection *protocol.Connection,
	driver agent.DriverFactory,
) (*agent.Runtime, error) {
	upstreams, err := deployment.Upstreams()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load the worker providers")
	}

	models, err := agent.NewRegistry(ctx, agent.RegistryOptions{
		Upstreams:        upstreams,
		DefaultModel:     deployment.DefaultModel,
		CompactionModel:  deployment.CompactionModel,
		MaxContextTokens: deployment.MaxContextTokens,
		Factory:          driver,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "discover the worker provider models")
	}

	resolver, err := harness.NewResolver(
		deployment.ConfigDirectory,
		agent.HarnessLimits(),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker harness resolver")
	}

	options := agent.RuntimeOptionsFromConfig(
		deployment,
		models,
		metrics.New(),
	)
	options.Store = connection.Store()
	options.Resolver = resolver
	options.WorkerGenerationID = document.GenerationID
	options.HookStateRoot = workerHookStateRoot(document)

	// The worker runs one session in one workspace, which is what makes its
	// runtime a single-workspace runtime rather than a second control plane.
	// It attaches to the session the controller already created rather than
	// resolving the workspace itself, because the workspace policy that decides
	// whether a directory may be opened lives in the control plane.
	options.DefaultWorkspace = connection.Ack.Workspace
	options.StartupSessionID = document.SessionID

	runtime, err := agent.NewRuntime(ctx, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker runtime")
	}

	return runtime, nil
}

// workerHookStateRoot keeps executable hook state inside the one writable
// session-private mount a worker receives. PEEN_CONFIG_DIR is read-only in a
// Docker worker and must remain source configuration only.
func workerHookStateRoot(document worker.LaunchDocument) string {
	return hooks.DefaultStateRoot(filepath.Dir(document.SocketPath))
}

// commandHandler answers the controller's commands.
//
// It is created before the connection so registration can complete, then given
// the runtime once that exists. A command arriving before the runtime is ready
// is refused rather than served against a half-built worker.
type commandHandler struct {
	runtime *agent.Runtime
	store   *protocol.Store

	// ready closes once the runtime exists. A worker registers before it can
	// build that runtime, because building it reads durable state over the
	// same connection, so the controller can send a command into the gap.
	ready     chan struct{}
	readyOnce sync.Once

	stopped  chan struct{}
	stopOnce sync.Once
}

func newCommandHandler() *commandHandler {
	return &commandHandler{
		ready:   make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (h *commandHandler) attach(
	runtime *agent.Runtime,
	store *protocol.Store,
) {
	h.runtime = runtime
	h.store = store

	h.readyOnce.Do(func() { close(h.ready) })
}

// awaitReady holds a command until the runtime exists.
//
// Waiting is right rather than refusing: the controller has already accepted
// this worker's registration, so the worker either finishes starting or dies,
// and a death closes the connection and fails the call with the real cause. A
// refusal here would turn a client's first message into an error purely
// because it arrived while the worker was still starting.
func (h *commandHandler) awaitReady(ctx context.Context) error {
	select {
	case <-h.ready:
		return nil
	case <-h.stopped:
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"the worker is shutting down",
		)
	case <-ctx.Done():
		return ctxerrors.Wrap(
			ctx.Err(),
			"waiting for the worker runtime to be ready",
		)
	}
}

// HandleCall is never served by a worker. The controller does not ask a worker
// for durable records; the traffic goes the other way.
func (h *commandHandler) HandleCall(
	_ context.Context,
	method string,
	_ json.RawMessage,
) (any, error) {
	return nil, ctxerrors.Wrapf(
		commerr.ErrPermissionDenied,
		"a controller may not call %q on a worker",
		method,
	)
}

// HandleCommand serves one controller command.
func (h *commandHandler) HandleCommand(
	ctx context.Context,
	command protocol.Command,
	payload json.RawMessage,
) (any, error) {
	// Shutdown is answered without a runtime. A controller that gave up on a
	// worker still needs the process to end.
	if command == protocol.CommandShutdown {
		h.stop()

		return protocol.Ack{}, nil
	}

	if err := h.awaitReady(ctx); err != nil {
		return nil, err
	}

	switch command {
	case protocol.CommandRunTurn:
		return h.runTurn(ctx, payload)
	case protocol.CommandCancel:
		return protocol.CancelResult{
			CancelRequested: h.store.RequestLocalCancellation(
				h.runtime.SessionID(),
			),
		}, nil
	case protocol.CommandSignalJob:
		return h.signalJob(ctx, payload)
	case protocol.CommandShutdown:
		// Answered above, before the runtime wait.
		h.stop()

		return protocol.Ack{}, nil
	default:
		return nil, ctxerrors.Wrapf(
			commerr.ErrNotImplemented,
			"worker command %q is not served",
			command,
		)
	}
}

// signalJob signals one of this worker's running jobs.
//
// The job's process group is a child of this process, so the controller cannot
// reach it. Running the runtime's own signal path here also records the
// durable signal request, which travels back over this same connection.
func (h *commandHandler) signalJob(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request := protocol.SignalJob{}
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"decode the signal_job command",
		)
	}

	response, err := h.runtime.SignalSessionJob(
		ctx,
		h.runtime.SessionID(),
		request.JobID,
		api.JobSignalRequest{
			Signal: api.JobSignalRequestSignal(request.Signal),
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "signal the worker job")
	}

	return protocol.SignalJobResult{
		Handled:   true,
		Signalled: response.Signalled,
		State:     string(response.State),
	}, nil
}

// runTurn runs one accepted user message to completion.
//
// The controller has already recorded the message, so the worker's own events
// reach clients through the durable writes it makes during the turn rather
// than a second delivery path.
func (h *commandHandler) runTurn(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request := protocol.RunTurn{}
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"decode the run_turn command",
		)
	}

	sessionID := h.runtime.SessionID()

	message := agent.MessageRequest{
		SessionID:    &sessionID,
		Message:      request.Message,
		SystemPrompt: systemPrompt(request),
	}
	if request.Model != "" {
		message.Model = &request.Model
	}

	result, err := h.runtime.RunMessage(ctx, message, request.RequestID, nil)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "run the session turn")
	}

	return protocol.TurnResult{Text: result.Text, Queued: result.Queued}, nil
}

func systemPrompt(request protocol.RunTurn) *agent.MessageSystemPrompt {
	if request.SystemPrompt == "" {
		return nil
	}

	return &agent.MessageSystemPrompt{
		Mode:    agent.PromptMode(request.PromptMode),
		Content: request.SystemPrompt,
	}
}

func (h *commandHandler) stop() {
	h.stopOnce.Do(func() { close(h.stopped) })
}
