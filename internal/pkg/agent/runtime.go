package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/essessey/elelemstream"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

// Runtime executes durable transport-neutral Peen turns.
type Runtime struct {
	store            *session.Store
	resolver         HarnessResolver
	models           ModelResolver
	rootAgent        string
	defaultModel     string
	defaultWorkspace string
	maxContextTokens int
	turnTimeout      time.Duration
	baseSystemPrompt string

	maxSystemPromptBytes int
	maxMessageBytes      int

	// turnSlots bounds concurrent turns across every session. A nil channel
	// disables the bound, which is what an embedding caller that wants none
	// gets.
	turnSlots chan struct{}

	compactionMode    config.CompactionMode
	compactionOptions compactionOptions

	toolLimits           tools.Limits
	maxToolRounds        int
	maxConcurrentTools   int
	toolTimeout          time.Duration
	maxToolResultTokens  int
	enableWorkspaceHooks bool
	hookCommandTimeout   time.Duration
	maxHookCommandOutput int
	eventBus             *events.Bus
	wakes                *wakeLimiter

	// jobsMutex guards jobs. A job registry is per SESSION, not per turn,
	// because a command started in one turn must still be visible, readable
	// and killable in a later one.
	jobsMutex sync.Mutex
	jobs      map[uuid.UUID]*tools.JobRegistry

	agentLimits     AgentRunLimits
	configDirectory string

	// agentRunsMutex guards agentRuns. An agent run registry is per SESSION,
	// exactly like jobs above, so a run started by one turn stays listable,
	// followable, and cancellable from a later one.
	agentRunsMutex sync.Mutex
	agentRuns      map[uuid.UUID]*AgentRunRegistry
}

// turnBasis is what one turn resolves before its durable record exists.
type turnBasis struct {
	workspace      string
	modelReference string
	model          ModelClient
	snapshot       harness.Snapshot
	systemPrompt   string
	contextHash    string
	promptHash     string
}

// promptAssembly is one turn's assembled prompt beside the map back to the
// durable rows it was built from.
type promptAssembly struct {
	prompt elelem.Prompt
	plan   reconstructionPlan
	active *models.Compaction
}

// turnOpening bundles what opening a turn's prompt produced, including any
// events the session accumulated while nothing was running.
type turnOpening struct {
	opened        *session.OpenSessionResult
	assembly      promptAssembly
	pendingBatch  events.Batch
	pendingEvents string
}

type preparedTurn struct {
	opened         *session.OpenSessionResult
	lease          session.Lease
	modelReference string
	model          ModelClient
	prompt         elelem.Prompt
	compactor      *compactor
	origin         *TurnOrigin

	// publisher fans one turn's protocol events to every destination the mode
	// needs. Both modes always include the transcript sink, so the durable
	// record does not depend on how the caller asked for the answer.
	publisher *essessey.Publisher
	// adapter turns Elelem's callbacks into correctly indexed content blocks.
	// The block arithmetic lives upstream so this package cannot get it wrong.
	adapter *elelemstream.Adapter

	workspace     string
	contextHash   string
	promptHash    string
	turn          *runtimeTurn
	executor      *tools.JobExecutor
	eventBus      *events.Bus
	pendingEvents string
	snapshot      harness.Snapshot
	toolHooks     *toolHookRuntime
}

// NewRuntime validates the dependencies shared by every Peen turn.
func NewRuntime(options RuntimeOptions) (*Runtime, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}

	if options.BaseSystemPrompt == "" {
		deploymentPrompt, err := LoadSystemPrompt(options.ConfigDirectory)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "load deployment system prompt")
		}

		options.BaseSystemPrompt = deploymentPrompt
	}

	if options.MaxSystemPromptBytes <= 0 {
		options.MaxSystemPromptBytes = defaultMaxSystemPromptBytes
	}

	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}

	options = options.withToolDefaults()

	compactionMode, compaction, err := compactionSettings(options)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		store:            options.Store,
		resolver:         options.Resolver,
		models:           options.Models,
		rootAgent:        options.RootAgent,
		defaultModel:     options.DefaultModel,
		defaultWorkspace: options.DefaultWorkspace,
		maxContextTokens: options.MaxContextTokens,
		turnTimeout:      options.TurnTimeout,
		baseSystemPrompt: options.BaseSystemPrompt,

		maxSystemPromptBytes: options.MaxSystemPromptBytes,
		maxMessageBytes:      options.MaxMessageBytes,
		turnSlots:            newTurnSlots(options.MaxConcurrentTurns),

		compactionMode:       compactionMode,
		compactionOptions:    compaction,
		toolLimits:           options.ToolLimits,
		maxToolRounds:        options.MaxToolRounds,
		maxConcurrentTools:   options.MaxConcurrentTools,
		toolTimeout:          options.ToolTimeout,
		maxToolResultTokens:  options.MaxToolResultTokens,
		enableWorkspaceHooks: options.EnableWorkspaceHooks,
		hookCommandTimeout:   options.HookCommandTimeout,
		maxHookCommandOutput: options.MaxHookCommandOutput,
		eventBus:             options.Events,
		wakes:                newWakeLimiter(options.MaxEventWakesPerHour),
		jobs:                 map[uuid.UUID]*tools.JobRegistry{},
		agentLimits:          options.AgentLimits.withDefaults(),
		configDirectory:      options.ConfigDirectory,
		agentRuns:            map[uuid.UUID]*AgentRunRegistry{},
	}, nil
}

// Run resolves context, records the turn, and runs the selected Elelem model.
func (r *Runtime) Run(
	ctx context.Context,
	input TurnRequest,
) (*TurnResult, error) {
	release, err := r.acquireTurnSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	prepared, err := r.prepareTurn(ctx, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare agent turn")
	}

	// A JSON turn publishes to the transcript alone. It builds the same
	// blocks an SSE turn does, so both store the same protocol records.
	prepared.attachPublisher(ctx)

	return r.runLease(ctx, prepared)
}

// attachPublisher builds the turn's publisher over the sinks given, always
// including the transcript sink, and binds a fresh adapter to it.
func (p *preparedTurn) attachPublisher(
	ctx context.Context,
	sinks ...essessey.Sink,
) {
	// The transcript sink goes first. MultiSink emits in order, and recording
	// an event is what triggers the progress frame describing it, so a
	// transcript sink placed last would let the content reach the client
	// before the status announcing it.
	ordered := append(
		[]essessey.Sink{transcriptSink{turn: p.turn}},
		sinks...,
	)

	p.publisher = essessey.NewPublisher(ctx, essessey.NewMultiSink(ordered...))
	p.adapter = elelemstream.New(p.publisher)
}

// newTurnSlots builds the global turn semaphore. A negative bound disables it
// entirely, which an embedding caller can ask for deliberately.
func newTurnSlots(maxConcurrentTurns int) chan struct{} {
	if maxConcurrentTurns < 0 {
		return nil
	}

	if maxConcurrentTurns == 0 {
		maxConcurrentTurns = defaultMaxConcurrentTurns
	}

	return make(chan struct{}, maxConcurrentTurns)
}

// acquireTurnSlot bounds how many turns run at once across every session.
//
// It waits rather than rejecting: the per-session lease already answers "this
// session is busy" with a conflict, and this bound exists to cap what the
// process does at once, not to turn away callers. A caller that will not wait
// cancels its own context, which is what ends the wait.
//
// Child agents do not take a slot. They run inside a parent turn that already
// holds one, so charging them again would let a deep enough chain deadlock
// against its own ancestors.
func (r *Runtime) acquireTurnSlot(ctx context.Context) (func(), error) {
	if r.turnSlots == nil {
		return func() {}, nil
	}

	select {
	case r.turnSlots <- struct{}{}:
		return func() { <-r.turnSlots }, nil
	case <-ctx.Done():
		return nil, ctxerrors.Wrap(ctx.Err(), "wait for a concurrent turn slot")
	}
}

//nolint:funlen // Turn creation has one contiguous failure-safe lease boundary.
func (r *Runtime) prepareTurn(
	ctx context.Context,
	input TurnRequest,
) (*preparedTurn, error) {
	basis, err := r.resolveTurnBasis(ctx, input)
	if err != nil {
		return nil, err
	}

	opening, err := r.openPromptWithEvents(
		ctx,
		input,
		basis.modelReference,
		basis.systemPrompt,
	)
	if err != nil {
		return nil, err
	}

	turn, lease, err := r.openTurn(ctx, input, basis.workspace, opening)
	if err != nil {
		return nil, err
	}

	executor, err := r.hostExecutor(
		basis.workspace,
		opening.opened.Session.ID,
		lease,
	)
	if err != nil {
		return nil, err
	}

	turnCompactor, err := r.newTurnCompactor(opening)
	if err != nil {
		return nil, err
	}

	prepared := &preparedTurn{
		opened:         opening.opened,
		lease:          lease,
		modelReference: basis.modelReference,
		model:          basis.model,
		prompt:         opening.assembly.prompt,
		compactor:      turnCompactor,
		origin:         input.Origin,
		workspace:      basis.workspace,
		contextHash:    basis.contextHash,
		promptHash:     basis.promptHash,
		turn:           turn,
		executor:       executor,
		eventBus:       r.eventBus,
		pendingEvents:  opening.pendingEvents,
		snapshot:       basis.snapshot,
	}

	toolHooks, err := r.newToolHookRuntime(prepared)
	if err != nil {
		return nil, r.finalizeFailedTurn(ctx, prepared, err)
	}

	prepared.toolHooks = toolHooks

	if opening.opened.Created {
		if err := prepared.runLifecycleHook(
			ctx,
			r,
			harness.HookEventSessionStart,
			map[string]bool{"created": true},
		); err != nil {
			return nil, r.finalizeFailedTurn(ctx, prepared, err)
		}
	}

	if err := prepared.runLifecycleHook(
		ctx,
		r,
		harness.HookEventPostUserMessage,
		userMessageHookInput(input, basis.workspace),
	); err != nil {
		return nil, r.finalizeFailedTurn(ctx, prepared, err)
	}

	return prepared, nil
}

// resolveTurnBasis settles everything a turn needs before its durable record
// exists, so nothing that can still be rejected happens after the lease.
func (r *Runtime) resolveTurnBasis(
	ctx context.Context,
	input TurnRequest,
) (turnBasis, error) {
	workspace, modelReference, err := r.resolveInput(input)
	if err != nil {
		return turnBasis{}, ctxerrors.Wrap(err, "resolve input")
	}

	model, err := r.models.ResolveModel(modelReference)
	if err != nil {
		return turnBasis{}, ctxerrors.Wrap(err, "resolve model")
	}

	snapshot, systemPrompt, contextHash, promptHash, err := r.resolveContext(
		ctx,
		input,
		workspace,
	)
	if err != nil {
		return turnBasis{}, ctxerrors.Wrap(err, "resolve context")
	}

	return turnBasis{
		workspace:      workspace,
		modelReference: modelReference,
		model:          model,
		snapshot:       snapshot,
		systemPrompt:   systemPrompt,
		contextHash:    contextHash,
		promptHash:     promptHash,
	}, nil
}

func (r *Runtime) resolveContext(
	ctx context.Context,
	input TurnRequest,
	workspace string,
) (harness.Snapshot, string, string, string, error) {
	snapshot, err := r.resolver.Resolve(workspace)
	if err != nil {
		return harness.Snapshot{}, "", "", "", ctxerrors.Wrap(
			err,
			"resolve harness context",
		)
	}

	systemPrompt, err := r.systemPrompt(snapshot, input, workspace)
	if err != nil {
		return harness.Snapshot{}, "", "", "", ctxerrors.Wrap(
			err,
			"build system prompt",
		)
	}

	systemPrompt, err = r.appendPreUserHookContext(
		ctx,
		snapshot,
		workspace,
		input,
		systemPrompt,
	)
	if err != nil {
		return harness.Snapshot{}, "", "", "", ctxerrors.Wrap(
			err,
			"run pre-user-message hooks",
		)
	}

	contextHash, promptHash, err := r.saveSnapshots(
		ctx,
		snapshot,
		systemPrompt,
		systemPrompt,
	)
	if err != nil {
		return harness.Snapshot{}, "", "", "", ctxerrors.Wrap(
			err,
			"save harness snapshots",
		)
	}

	return snapshot, systemPrompt, contextHash, promptHash, nil
}

func (r *Runtime) openPrompt(
	ctx context.Context,
	input TurnRequest,
	modelReference string,
	systemPrompt string,
) (*session.OpenSessionResult, promptAssembly, error) {
	opened, err := r.store.CreateOrResume(
		ctx,
		input.SessionID,
		session.OpenSessionOptions{
			RootAgent: r.rootAgent,
			ModelID:   modelReference,
		},
	)
	if err != nil {
		return nil, promptAssembly{}, ctxerrors.Wrap(err, "open session")
	}

	history, err := r.store.CompletedHistory(ctx, opened.Session.ID)
	if err != nil {
		return nil,
			promptAssembly{},
			ctxerrors.Wrap(err, "load completed history")
	}

	prompt, plan, err := promptFromHistory(systemPrompt, history)
	if err != nil {
		return nil,
			promptAssembly{},
			ctxerrors.Wrap(err, "assemble persisted history")
	}

	return opened, promptAssembly{
		prompt: prompt.UserText(input.Message),
		plan:   plan,
		active: history.Compaction,
	}, nil
}

// hostExecutor builds the turn's tool executor over a session-scoped job
// registry. The registry outlives the turn on purpose: a background command
// started here must still be found and killed from a later turn.
func (r *Runtime) hostExecutor(
	workspace string,
	sessionID uuid.UUID,
	lease session.Lease,
) (*tools.JobExecutor, error) {
	executor, err := tools.NewExecutor(tools.Options{
		Workspace: workspace,
		Limits:    r.toolLimits,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create host tool executor")
	}

	registry, err := r.sessionJobs(sessionID)
	if err != nil {
		return nil, err
	}

	jobExecutor, err := tools.NewJobExecutor(executor, registry, lease.TurnID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create host job executor")
	}

	return jobExecutor, nil
}

// launchAgentDeps bundles what this turn's launch_agent tool needs to run a
// child conversation sharing this turn's session, workspace, resolved
// rules, tool registry, and model.
func (r *Runtime) launchAgentDeps(prepared *preparedTurn) *launchAgentDeps {
	return &launchAgentDeps{
		runtime:        r,
		executor:       prepared.executor,
		snapshot:       prepared.snapshot,
		model:          prepared.model,
		modelReference: prepared.modelReference,
		sessionID:      prepared.opened.Session.ID,
		parentTurnID:   prepared.lease.TurnID,
	}
}

// sessionJobs returns the session's job registry, creating it on first use.
func (r *Runtime) sessionJobs(
	sessionID uuid.UUID,
) (*tools.JobRegistry, error) {
	r.jobsMutex.Lock()
	defer r.jobsMutex.Unlock()

	if registry, ok := r.jobs[sessionID]; ok {
		return registry, nil
	}

	registry, err := tools.NewJobRegistry(
		sessionID,
		r.jobPublisher(),
		r.toolLimits,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create session job registry")
	}

	r.jobs[sessionID] = registry

	return registry, nil
}

// jobPublisher hands the job registry the event bus so completion reaches the
// model through the one event path. A nil bus means jobs publish nothing.
//
// It returns the interface rather than *events.Bus on purpose: a nil *Bus
// stored in an interface is not a nil interface, so returning the concrete
// type would hand the registry a non-nil publisher that panics on use.
//
//nolint:ireturn // See above: nil-ness must survive the return.
func (r *Runtime) jobPublisher() tools.EventPublisher {
	if r.eventBus == nil {
		return nil
	}

	return r.eventBus
}

// ShutdownJobs stops every supervised process this runtime started. Jobs are
// children of this process, so none may silently outlive the service.
func (r *Runtime) ShutdownJobs(ctx context.Context) error {
	r.jobsMutex.Lock()
	registries := make([]*tools.JobRegistry, 0, len(r.jobs))

	for _, registry := range r.jobs {
		registries = append(registries, registry)
	}

	r.jobs = map[uuid.UUID]*tools.JobRegistry{}
	r.jobsMutex.Unlock()

	failures := make([]error, 0, len(registries))

	for _, registry := range registries {
		if err := registry.Shutdown(ctx); err != nil {
			failures = append(failures, err)
		}
	}

	if len(failures) > 0 {
		return ctxerrors.Wrap(
			errors.Join(failures...),
			"shut down session jobs",
		)
	}

	return nil
}

// openPromptWithEvents builds the turn's prompt and folds in whatever the
// session accumulated while nothing was running. Events are delivered up front
// rather than only at a tool boundary, because a turn that never calls a tool
// would otherwise never learn what happened while the session was idle.
func (r *Runtime) openPromptWithEvents(
	ctx context.Context,
	input TurnRequest,
	modelReference string,
	systemPrompt string,
) (turnOpening, error) {
	opened, assembly, err := r.openPrompt(
		ctx,
		input,
		modelReference,
		systemPrompt,
	)
	if err != nil {
		return turnOpening{}, ctxerrors.Wrap(err, "open prompt")
	}

	opening := turnOpening{
		opened:       opened,
		assembly:     assembly,
		pendingBatch: r.drainPendingEvents(opened.Session.ID),
	}

	if len(opening.pendingBatch.Notices) == 0 {
		return opening, nil
	}

	opening.pendingEvents = renderSessionEvents(opening.pendingBatch)
	opening.assembly.prompt = assembly.prompt.UserText(opening.pendingEvents)

	return opening, nil
}

// openTurn acquires the durable turn and publishes whatever was already
// waiting for the session, so the first thing the stream carries is what the
// agent missed.
func (r *Runtime) openTurn(
	ctx context.Context,
	input TurnRequest,
	workspace string,
	opening turnOpening,
) (*runtimeTurn, session.Lease, error) {
	turn, lease, err := r.startTurn(
		ctx,
		input,
		opening.opened.Session.ID,
		workspace,
		opening.pendingEvents,
	)
	if err != nil {
		return nil, session.Lease{}, ctxerrors.Wrap(err, "start durable turn")
	}

	if len(opening.pendingBatch.Notices) == 0 {
		return turn, lease, nil
	}

	if err := turn.emit(EventTypeSessionEvents, sessionEventsPayload{
		Notices: opening.pendingBatch.Notices,
		Dropped: opening.pendingBatch.Dropped,
	}); err != nil {
		return nil, session.Lease{}, ctxerrors.Wrap(
			err,
			"emit pending session events",
		)
	}

	return turn, lease, nil
}

// drainPendingEvents takes whatever the session accumulated while nothing was
// running, so the turn can deliver it before its first model call.
func (r *Runtime) drainPendingEvents(sessionID uuid.UUID) events.Batch {
	if r.eventBus == nil {
		return events.Batch{}
	}

	return r.eventBus.Drain(sessionID)
}

func (r *Runtime) startTurn(
	ctx context.Context,
	input TurnRequest,
	sessionID uuid.UUID,
	workspace string,
	pendingEvents string,
) (*runtimeTurn, session.Lease, error) {
	requestID := input.RequestID
	if requestID == uuid.Nil {
		requestID = uuid.New()
	}

	turn := runtimeTurn{requestID: requestID, sink: input.OnEvent}

	messages := make([]session.MessageInput, 0, turnStartMessageCapacity)
	if pendingEvents != "" {
		messages = append(messages, session.MessageInput{
			Role:      models.MessageRoleUser,
			Content:   pendingEvents,
			Workspace: workspace,
		})
	}

	messages = append(messages, session.MessageInput{
		Role:    models.MessageRoleUser,
		Content: input.Message,
	})

	lease, err := r.store.AcquireTurn(
		ctx,
		sessionID,
		session.StartTurnInput{
			RequestID: requestID,
			Workspace: workspace,
			Messages:  messages,
		},
	)
	if err != nil {
		return nil, session.Lease{}, ctxerrors.Wrap(err, "acquire session turn")
	}

	// The user and pending-event messages went to AcquireTurn directly, not
	// through turn.messages, so both checkpoint marks correctly start at zero.
	turn.store = r.store
	turn.lease = lease

	return &turn, lease, nil
}

func (r *Runtime) resolveInput(input TurnRequest) (string, string, error) {
	if err := r.validateTurnInput(input); err != nil {
		return "", "", err
	}

	workspace := input.Workspace
	if workspace == "" {
		workspace = r.defaultWorkspace
	}

	modelReference := input.Model
	if modelReference == "" {
		modelReference = r.defaultModel
	}

	return workspace, modelReference, nil
}

// validateTurnInput rejects a request the runtime cannot run.
//
// These checks live here rather than in the HTTP handler because the runtime
// is also reachable from an embedding Go caller, which never passes through
// the OpenAPI validator or any middleware.
func (r *Runtime) validateTurnInput(input TurnRequest) error {
	if strings.TrimSpace(input.Message) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "message")
	}

	if input.SystemPrompt == "" && input.SystemPromptMode != "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"system prompt mode requires a system prompt",
		)
	}

	if input.SystemPromptMode != "" &&
		input.SystemPromptMode != PromptModeAppend &&
		input.SystemPromptMode != PromptModeReplace {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown system prompt mode",
		)
	}

	if len(input.Message) > r.maxMessageBytes {
		return ctxerrors.Wrapf(
			ErrMessageTooLarge,
			"%d bytes exceeds the %d byte limit",
			len(input.Message),
			r.maxMessageBytes,
		)
	}

	if len(input.SystemPrompt) > r.maxSystemPromptBytes {
		return ctxerrors.Wrapf(
			ErrSystemPromptTooLarge,
			"%d bytes exceeds the %d byte limit",
			len(input.SystemPrompt),
			r.maxSystemPromptBytes,
		)
	}

	return nil
}

func (r *Runtime) systemPrompt(
	snapshot harness.Snapshot,
	input TurnRequest,
	workspace string,
) (string, error) {
	blocks, err := snapshot.PromptBlocks(r.rootAgent)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve prompt blocks")
	}

	base := r.baseSystemPrompt
	if input.SystemPromptMode == PromptModeReplace {
		base = input.SystemPrompt
	}

	sections := make(
		[]string,
		0,
		len(blocks)+promptSectionAdditionalCapacity,
	)

	sections = append(sections, base)
	if input.SystemPromptMode == PromptModeAppend {
		sections = append(sections, input.SystemPrompt)
	}

	for _, block := range blocks {
		sections = append(sections, block.Content)
	}

	workspaceBlock, err := workspaceMetadataBlock(workspace)
	if err != nil {
		return "", err
	}

	sections = append(sections, workspaceBlock)

	content := strings.Join(sections, systemSectionGap)

	return content, nil
}

// workspaceMetadataBlock states the directory relative tool paths resolve
// from. It is JSON-encoded because a workspace path can contain quotes,
// newlines, or anything else a filesystem allows, and pasting one raw into the
// prompt is how a directory name becomes an instruction.
func workspaceMetadataBlock(workspace string) (string, error) {
	encoded, err := json.Marshal(workspace)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode workspace metadata")
	}

	return workspaceMetadataLead + string(encoded), nil
}

func (r *Runtime) saveSnapshots(
	ctx context.Context,
	snapshot harness.Snapshot,
	contextContent string,
	systemPrompt string,
) (string, string, error) {
	manifest, err := json.Marshal(snapshot.Manifest())
	if err != nil {
		return "", "", ctxerrors.Wrap(err, "marshal context manifest")
	}

	contextHash := hash(strings.Join(
		[]string{snapshot.Hash(), contextContent},
		systemSectionGap,
	))
	if err := r.store.SaveContextSnapshot(ctx, &models.ContextSnapshot{
		Hash:            contextHash,
		ManifestJSON:    string(manifest),
		ResolvedContent: contextContent,
	}); err != nil {
		return "", "", ctxerrors.Wrap(err, "save context snapshot")
	}

	promptHash, err := r.savePromptSnapshot(ctx, systemPrompt)
	if err != nil {
		return "", "", ctxerrors.Wrap(err, "save prompt snapshot")
	}

	return contextHash, promptHash, nil
}

func (r *Runtime) savePromptSnapshot(
	ctx context.Context,
	systemPrompt string,
) (string, error) {
	promptHash := hash(systemPrompt)
	if err := r.store.SavePromptSnapshot(ctx, &models.PromptSnapshot{
		Hash:            promptHash,
		EffectivePrompt: systemPrompt,
	}); err != nil {
		return "", ctxerrors.Wrap(err, "save prompt snapshot")
	}

	return promptHash, nil
}

func (r *Runtime) runLease(
	ctx context.Context,
	prepared *preparedTurn,
) (*TurnResult, error) {
	ctx = prepared.scopedContext(ctx)

	turnContext, cancel := context.WithCancel(ctx)
	defer cancel()

	r.store.RegisterCancellation(prepared.lease, cancel)

	if err := r.startLease(ctx, prepared); err != nil {
		return nil, r.finalizeFailedTurn(ctx, prepared, err)
	}

	response, err := r.runProvider(turnContext, prepared)
	if err != nil {
		return nil, r.finalizeFailedTurn(ctx, prepared, err)
	}

	result, err := r.completeLease(ctx, prepared, response)
	if err != nil {
		return nil, r.finalizeFailedTurn(ctx, prepared, err)
	}

	return result, nil
}

func (r *Runtime) startLease(
	ctx context.Context,
	prepared *preparedTurn,
) error {
	if err := prepared.runLifecycleHook(
		ctx,
		r,
		harness.HookEventTurnStart,
		turnHookInput(prepared),
	); err != nil {
		return ctxerrors.Wrap(err, "run turn-start hooks")
	}

	payload := turnStartedPayload{
		SessionID: prepared.opened.Session.ID.String(),
		Model:     prepared.modelReference,
		Workspace: prepared.workspace,
	}
	if prepared.origin != nil {
		payload.OriginEventID = prepared.origin.EventID.String()
		payload.OriginEventType = prepared.origin.EventType
	}

	if err := prepared.turn.emit(EventTypeTurnStarted, payload); err != nil {
		return ctxerrors.Wrap(err, "emit turn started")
	}

	ctxscope.GetLogger(ctx).Info(
		"agent turn started",
		"model", prepared.modelReference,
		"workspace", prepared.workspace,
	)

	return nil
}

func (r *Runtime) runProvider(
	ctx context.Context,
	prepared *preparedTurn,
) (*elelem.Response, error) {
	toolSet := hostToolSet(
		prepared.executor,
		prepared.injectSessionEvents,
		prepared.snapshot,
	)
	toolSet.Add(launchAgentTool(
		r.launchAgentDeps(prepared),
		prepared.injectSessionEvents,
	))
	bindToolHooks(toolSet, prepared.toolHooks)

	request := elelem.NewRequest(prepared.model.Client).
		WithModel(prepared.model.Model).
		WithPrompt(prepared.prompt).
		WithMaxContextTokens(r.maxContextTokens).
		WithTimeout(r.turnTimeout).
		WithTools(toolSet).
		WithAutoToolCalls().
		WithMaxRounds(r.maxToolRounds).
		WithMaxConcurrentTools(r.maxConcurrentTools).
		WithToolTimeout(r.toolTimeout).
		WithMaxToolResultTokens(r.maxToolResultTokens).
		OnRetry(prepared.onRetry).
		OnAssistantMessage(prepared.onAssistantMessage).
		OnToolCallStart(prepared.onToolCallStart).
		OnToolResult(prepared.onToolResult).
		OnMessageInjection(prepared.onMessageInjection)

	// Bind appends its callbacks rather than replacing, so the adapter's block
	// production and Peen's own durability hooks both run. Peen no longer
	// emits its own delta events: the adapter publishes content blocks and the
	// transcript sink stores them under the names that went on the wire.
	request = prepared.adapter.Bind(request)

	// Under drop-oldest no hook is installed at all, so Elelem applies its own
	// whole-unit eviction. Installing one and calling DropOldestUnits from it
	// would run both policies against the same request.
	if prepared.compactor != nil {
		request = request.PreMaxTokensReached(prepared.compactor.handle)
	}

	response, err := request.Run(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "run provider request")
	}

	return response, nil
}

// newTurnCompactor returns the turn's compaction hook, or nil under
// drop-oldest where Elelem's own eviction is the policy.
func (r *Runtime) newTurnCompactor(
	opening turnOpening,
) (*compactor, error) {
	if r.compactionMode != config.CompactionModeSummarize {
		//nolint:nilnil // Drop-oldest deliberately installs no hook.
		return nil, nil
	}

	built, err := newCompactor(
		r.compactionOptions,
		opening.opened.Session.ID,
		opening.assembly.plan,
		opening.assembly.active,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create turn compactor")
	}

	return built, nil
}

// compactionSettings resolves the summary-only settings and the deployment's
// summarizer instructions.
//
// They are resolved and validated even under drop-oldest, so switching modes
// never exposes a configuration that startup previously accepted but cannot
// use. Reading COMPACTION.md is the exception: a deployment that will never
// summarize is not blocked by a file it will never send.
func compactionSettings(
	options RuntimeOptions,
) (config.CompactionMode, compactionOptions, error) {
	mode, err := resolveCompactionMode(options.CompactionMode)
	if err != nil {
		return "", compactionOptions{}, err
	}

	settings := compactionOptions{
		Store:           options.Store,
		Models:          options.Models,
		ModelReference:  options.CompactionModel,
		MaxOutputTokens: options.CompactionOutputTokens,
		Timeout:         options.CompactionTimeout,
	}.withDefaults(options.DefaultModel)

	if settings.MaxOutputTokens >= options.MaxContextTokens {
		return "", compactionOptions{}, ctxerrors.Wrapf(
			ErrInvalidCompactionOptions,
			"a %d token summary allowance does not fit a %d token budget",
			settings.MaxOutputTokens,
			options.MaxContextTokens,
		)
	}

	prompt, err := resolveCompactionPrompt(mode, options)
	if err != nil {
		return "", compactionOptions{}, err
	}

	settings.Prompt = prompt
	settings.PromptHash = compactionPromptHash(prompt)

	if err := settings.validate(); err != nil {
		return "", compactionOptions{}, ctxerrors.Wrap(
			err,
			"resolve compaction settings",
		)
	}

	return mode, settings, nil
}

func resolveCompactionMode(
	mode config.CompactionMode,
) (config.CompactionMode, error) {
	if mode == "" {
		return config.CompactionModeDropOldest, nil
	}

	if mode != config.CompactionModeDropOldest &&
		mode != config.CompactionModeSummarize {
		return "", ctxerrors.Wrapf(
			ErrInvalidCompactionOptions,
			"unknown compaction mode %q",
			mode,
		)
	}

	return mode, nil
}

func resolveCompactionPrompt(
	mode config.CompactionMode,
	options RuntimeOptions,
) (string, error) {
	if options.CompactionPrompt != "" {
		return options.CompactionPrompt, nil
	}

	if mode != config.CompactionModeSummarize {
		return defaultCompactionPrompt, nil
	}

	prompt, err := LoadCompactionPrompt(options.ConfigDirectory)
	if err != nil {
		return "", ctxerrors.Wrap(err, "load compaction prompt")
	}

	return prompt, nil
}

func (r *Runtime) completeLease(
	ctx context.Context,
	prepared *preparedTurn,
	response *elelem.Response,
) (*TurnResult, error) {
	if err := prepared.runLifecycleHook(
		ctx,
		r,
		harness.HookEventTurnStop,
		turnStopHookInput(prepared, response),
	); err != nil {
		return nil, ctxerrors.Wrap(err, "run turn-stop hooks")
	}

	if err := prepared.turn.emit(EventTypeTurnCompleted, turnCompletedPayload{
		Model: prepared.modelReference,
		Text:  response.Text,
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "emit turn completed")
	}

	// Only the tail: everything before the last checkpoint is already durable,
	// and writing it again would duplicate the whole turn.
	pending := prepared.turn.pendingTranscript()

	if err := r.store.FinalizeTurn(
		context.WithoutCancel(ctx),
		prepared.lease,
		session.FinalizeTurnInput{
			State:               models.TurnStateCompleted,
			ContextSnapshotHash: &prepared.contextHash,
			PromptSnapshotHash:  &prepared.promptHash,
			Messages:            pending.messages,
			Events:              pending.events,
		},
	); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize completed turn")
	}

	ctxscope.GetLogger(ctx).Info(
		"agent turn completed",
		"model", prepared.modelReference,
		"events", len(prepared.turn.events),
	)

	return &TurnResult{
		SessionID:    prepared.opened.Session.ID,
		Created:      prepared.opened.Created,
		Text:         response.Text,
		Thinking:     response.Reasoning,
		Model:        prepared.modelReference,
		Events:       append([]Event(nil), prepared.turn.events...),
		FinishReason: response.FinishReason,
		HasToolCalls: len(response.ToolCalls) > 0,
		OutputTokens: response.Usage.Completion,
	}, nil
}

func (r *Runtime) finalizeFailedTurn(
	ctx context.Context,
	prepared *preparedTurn,
	runErr error,
) error {
	state, classification, eventType := failedTurnState(runErr)
	if state == models.TurnStateCancelled {
		hookErr := prepared.runLifecycleHook(
			context.WithoutCancel(ctx),
			r,
			harness.HookEventTurnCancelled,
			map[string]string{"reason": classification},
		)
		if hookErr != nil {
			runErr = errors.Join(
				runErr,
				ctxerrors.Wrap(hookErr, "run turn-cancelled hooks"),
			)
		}
	}

	eventErr := prepared.turn.emit(
		eventType,
		turnFailedPayload{Reason: classification},
	)
	if eventErr != nil {
		runErr = errors.Join(runErr, eventErr)
	}

	// Only the tail, for the same reason as the completed path. FinalizeTurn
	// marks every message of a non-completed turn incomplete, including the
	// ones an earlier checkpoint already wrote.
	pending := prepared.turn.pendingTranscript()

	finalizeErr := r.store.FinalizeTurn(
		context.WithoutCancel(ctx),
		prepared.lease,
		session.FinalizeTurnInput{
			State:                 state,
			FailureClassification: classification,
			ContextSnapshotHash:   &prepared.contextHash,
			PromptSnapshotHash:    &prepared.promptHash,
			Messages:              markIncomplete(pending.messages),
			Events:                pending.events,
		},
	)
	if finalizeErr != nil {
		wrappedFinalizeErr := ctxerrors.Wrap(
			finalizeErr,
			"finalize failed turn",
		)
		runErr = errors.Join(runErr, wrappedFinalizeErr)
	}

	ctxscope.GetLogger(ctx).Warn(
		"agent turn failed",
		"classification", classification,
		"err", runErr,
	)

	return runErr
}

func (p *preparedTurn) scopedContext(ctx context.Context) context.Context {
	return ctxscope.Set(
		ctx,
		ctxscope.Attr("session_id", p.opened.Session.ID.String()),
		ctxscope.Attr("turn_id", p.lease.TurnID.String()),
		ctxscope.Attr("request_id", p.turn.requestID.String()),
	)
}

func (p *preparedTurn) onRetry(
	_ context.Context,
	attempt elelem.RetryAttempt,
) error {
	return p.turn.emit(EventTypeProviderRetry, providerRetryPayload{
		Attempt: attempt.Attempt,
		Reason:  attempt.Reason,
		Status:  attempt.Status,
		DelayMS: attempt.Delay.Milliseconds(),
	})
}

func (p *preparedTurn) onAssistantMessage(
	ctx context.Context,
	message elelem.Message,
) error {
	input, err := messageInput(message, p.modelReference)
	if err != nil {
		return ctxerrors.Wrap(err, "convert assistant message")
	}

	p.turn.appendMessage(input)
	p.turn.checkpointOrLog(ctx)

	return nil
}

// onMessageInjection records an injected message durably. Elelem adds it to
// the model's transcript, but a resumed session rebuilds context from Peen's
// own messages, so without this the agent would forget it was ever told.
func (p *preparedTurn) onMessageInjection(
	_ context.Context,
	injection elelem.MessageInjection,
) error {
	role, err := injectedMessageRole(injection.Type)
	if err != nil {
		return err
	}

	p.turn.appendMessage(session.MessageInput{
		Role:      role,
		Content:   injection.Content,
		Workspace: p.workspace,
	})

	return nil
}

func injectedMessageRole(role elelem.Role) (models.MessageRole, error) {
	switch role {
	case elelem.RoleUser:
		return models.MessageRoleUser, nil
	case elelem.RoleAssistant:
		return models.MessageRoleAssistant, nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unsupported injected message role",
		)
	}
}

// onToolCallStart publishes the tool_use block before the handler runs. The
// call ID is Elelem's, so the durable event, the transcript tool message, and
// the SSE block all share it.
func (p *preparedTurn) onToolCallStart(
	ctx context.Context,
	call elelem.ToolCallEvent,
) error {
	p.turn.markToolStarted(call.CallID)

	ctxscope.GetLogger(ctx).Debug(
		"host tool call started",
		"tool_name", call.Name,
		"tool_call_id", call.CallID,
		"argument_bytes", len(call.Arguments),
	)

	return p.turn.emit(EventTypeToolUse, toolUsePayload{
		CallID:    call.CallID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
}

// onToolResult publishes the tool_result block and records the durable
// tool-role transcript message for the same call ID.
func (p *preparedTurn) onToolResult(
	ctx context.Context,
	call elelem.ToolCallEvent,
) error {
	content := ""
	isError := false

	if call.Result != nil {
		content = call.Result.Content
		isError = call.Result.IsError
	}

	p.turn.appendMessage(session.MessageInput{
		Role:       models.MessageRoleTool,
		Content:    content,
		ToolCallID: call.CallID,
		IsError:    isError,
		Workspace:  p.workspace,
	})

	ctxscope.GetLogger(ctx).Info(
		"host tool call completed",
		"tool_name", call.Name,
		"tool_call_id", call.CallID,
		"is_error", isError,
		"result_bytes", len(content),
		"duration_ms", p.turn.toolDuration(call.CallID).Milliseconds(),
	)

	// Checkpoint before publishing, not after. A finished tool call closes a
	// unit, and the client must never learn the unit closed before the rows
	// backing it are durable.
	p.turn.checkpointOrLog(ctx)

	return p.turn.emit(EventTypeToolResult, toolResultPayload{
		CallID:  call.CallID,
		Name:    call.Name,
		Content: content,
		IsError: isError,
	})
}

func failedTurnState(err error) (models.TurnState, string, string) {
	if errors.Is(err, context.Canceled) {
		return models.TurnStateCancelled,
			failureClassCancelled,
			EventTypeTurnCancelled
	}

	return models.TurnStateFailed, failureClassAgentRun, EventTypeTurnFailed
}

// promptFromHistory assembles the durable history and the map back to the rows
// it came from, so compaction can name a database range instead of guessing
// one from message text.
func promptFromHistory(
	systemPrompt string,
	history *session.History,
) (elelem.Prompt, reconstructionPlan, error) {
	prompt := elelem.NewPrompt().WithSystem(systemPrompt)

	if history.Compaction != nil {
		prompt = prompt.Add(compactionSummaryMessage(
			compactionSummaryLead + history.Compaction.Summary,
		))
	}

	messages := make([]elelem.Message, 0, len(history.Messages))
	for _, message := range history.Messages {
		converted, err := elelemMessage(message)
		if err != nil {
			return elelem.Prompt{}, reconstructionPlan{}, ctxerrors.Wrap(
				err,
				"convert persisted message",
			)
		}

		messages = append(messages, converted)
	}

	plan, err := buildReconstructionPlan(
		planLeadCount(systemPrompt, history),
		history.Compaction != nil,
		history.Messages,
	)
	if err != nil {
		return elelem.Prompt{}, reconstructionPlan{}, ctxerrors.Wrap(
			err,
			"build reconstruction plan",
		)
	}

	return prompt.WithHistory(messages), plan, nil
}

func elelemMessage(message *models.Message) (elelem.Message, error) {
	if message == nil {
		return elelem.Message{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil history message",
		)
	}

	role, err := elelemRole(message.Role)
	if err != nil {
		return elelem.Message{}, err
	}

	toolCalls := []elelem.ToolCall{}

	if err := json.Unmarshal(
		[]byte(message.ToolCallsJSON),
		&toolCalls,
	); err != nil {
		return elelem.Message{}, ctxerrors.Wrap(
			err,
			"unmarshal stored tool calls",
		)
	}

	return elelem.Message{
		Role:              role,
		Content:           elelem.Text(message.Content),
		ToolCalls:         toolCalls,
		ToolCallID:        message.ToolCallID,
		ToolResultIsError: message.IsError,
		Reasoning:         message.Thinking,
	}, nil
}

func elelemRole(role models.MessageRole) (elelem.Role, error) {
	switch role {
	case models.MessageRoleUser:
		return elelem.RoleUser, nil
	case models.MessageRoleAssistant:
		return elelem.RoleAssistant, nil
	case models.MessageRoleTool:
		return elelem.RoleTool, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"unknown message role %q",
			role,
		)
	}
}

func messageInput(
	message elelem.Message,
	modelReference string,
) (session.MessageInput, error) {
	toolCalls, err := json.Marshal(message.ToolCalls)
	if err != nil {
		return session.MessageInput{}, ctxerrors.Wrap(
			err,
			"marshal assistant tool calls",
		)
	}

	return session.MessageInput{
		Role:          models.MessageRoleAssistant,
		Content:       message.Text(),
		ModelID:       modelReference,
		Thinking:      message.Reasoning,
		ToolCallsJSON: string(toolCalls),
	}, nil
}

func markIncomplete(messages []session.MessageInput) []session.MessageInput {
	marked := append([]session.MessageInput(nil), messages...)
	for index := range marked {
		marked[index].Incomplete = true
	}

	return marked
}

// emit records one event and forwards it to the caller's sink. Serialized so
// concurrent tool hooks cannot interleave the durable event order or the sink
// writes.
func (t *runtimeTurn) emit(eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ctxerrors.Wrap(err, "marshal runtime event")
	}

	return t.record(Event{Type: eventType, Payload: encoded})
}

// emitProtocol records one event exactly as it went on the wire, keeping the
// essessey event name rather than translating it into a Peen name.
func (t *runtimeTurn) emitProtocol(event essessey.Event) error {
	return t.record(Event{Type: event.Event, Payload: event.Data})
}

func (t *runtimeTurn) record(event Event) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Progress is advertised before the event it describes, so a client never
	// sees a content block arrive while the stream still claims to be waiting.
	if t.statusReporter != nil {
		if err := t.statusReporter.report(event.Type); err != nil {
			return err
		}
	}

	t.events = append(t.events, event)
	if t.sink == nil {
		return nil
	}

	return t.sink(event)
}

func (t *runtimeTurn) appendMessage(message session.MessageInput) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.messages = append(t.messages, message)
}

// markToolStarted records the call's start so the result hook can report a
// duration. Returns the elapsed time and clears the entry.
func (t *runtimeTurn) markToolStarted(callID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.toolStart == nil {
		t.toolStart = map[string]time.Time{}
	}

	t.toolStart[callID] = time.Now()
}

func (t *runtimeTurn) toolDuration(callID string) time.Duration {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	started, ok := t.toolStart[callID]
	if !ok {
		return 0
	}

	delete(t.toolStart, callID)

	return time.Since(started)
}

// eventInputs converts the events from index onward into durable rows. The
// caller holds mutex.
func (t *runtimeTurn) eventInputs(from int) []session.EventInput {
	pending := t.events[from:]

	inputs := make([]session.EventInput, 0, len(pending))
	for _, event := range pending {
		inputs = append(inputs, session.EventInput{
			RequestID:   t.requestID,
			EventType:   event.Type,
			PayloadJSON: string(event.Payload),
		})
	}

	return inputs
}

// pendingTranscript returns everything produced since the last checkpoint,
// along with the marks to advance once it is durable.
func (t *runtimeTurn) pendingTranscript() pendingTranscript {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	return pendingTranscript{
		messages: append(
			[]session.MessageInput(nil),
			t.messages[t.checkpointedMessages:]...,
		),
		events:       t.eventInputs(t.checkpointedEvents),
		nextMessages: len(t.messages),
		nextEvents:   len(t.events),
	}
}

// checkpoint makes everything produced since the last checkpoint durable.
//
// It runs at unit boundaries, a completed assistant message or a finished tool
// call, rather than per streamed delta. A delta is only ever replayed as part
// of the block it belongs to, and prompt reconstruction never concatenates
// stored deltas, so committing each one separately would buy no recoverable
// state and would serialize the stream behind the disk. What it does buy is
// that a process killed mid-turn keeps every tool call and assistant message
// the turn already produced, instead of losing all of them.
func (t *runtimeTurn) checkpoint(ctx context.Context) error {
	if t.store == nil {
		return nil
	}

	t.checkpointMutex.Lock()
	defer t.checkpointMutex.Unlock()

	pending := t.pendingTranscript()
	if pending.isEmpty() {
		return nil
	}

	err := t.store.AppendCheckpoint(
		context.WithoutCancel(ctx),
		t.lease,
		pending.messages,
		pending.events,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "append turn checkpoint")
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.checkpointedMessages = pending.nextMessages
	t.checkpointedEvents = pending.nextEvents

	return nil
}

// checkpointOrLog makes progress durable on a callback path that must not fail
// the turn for a checkpoint problem. Losing a checkpoint degrades crash
// recovery; aborting the turn loses the work outright, which is worse.
func (t *runtimeTurn) checkpointOrLog(ctx context.Context) {
	if err := t.checkpoint(ctx); err != nil {
		ctxscope.GetLogger(ctx).Error(
			"turn checkpoint failed, continuing",
			"reason", reasonCheckpointFailed,
			"err", err,
		)
	}
}

func hash(value string) string {
	digest := sha256.Sum256([]byte(value))

	return hex.EncodeToString(digest[:])
}
