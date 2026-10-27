package agent

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	directoryReplayPageSize = 128

	changeDirectoryDescription = `Change this agent's current directory.
Load the target's AGENTS.md, rules, skills, agents, hooks and event handlers.
Later relative file paths and commands resolve from this directory.
Children inherit it and can change their own directory independently.
Use this tool for persistent directory hops. A shell-local cd does not persist.
Call it alone in a tool round, before calls that depend on the target directory.
The directory must already exist. A failed hop leaves the current scope intact.
The result lists the instruction sources the hop loaded, unloaded and changed;
their text is in your instructions from the next step on.`
	changeDirectorySchema = `{
  "type": "object",
  "properties": {"directory": {"type": "string", "minLength": 1}},
  "required": ["directory"],
  "additionalProperties": false
}`
)

type directoryResolver interface {
	ResolveDirectory(string) (harness.Snapshot, error)
}

type changeDirectoryInput struct {
	Directory string `json:"directory"`
}

type directoryMarker struct {
	Directory   string `json:"directory"`
	ContextHash string `json:"contextHash"`
	PromptHash  string `json:"promptHash"`
}

// changeDirectoryResult is what changeDirectory returns to the model. The
// source lists name what the hop added to, removed from, or changed in the
// instructions, so a model that reads only the result still learns them.
type changeDirectoryResult struct {
	directoryMarker

	Loaded   []harnessSource `json:"loaded,omitempty"`
	Unloaded []harnessSource `json:"unloaded,omitempty"`
	Changed  []harnessSource `json:"changed,omitempty"`
}

// harnessSource is one harness source a directory hop added or removed.
type harnessSource struct {
	Kind   harness.SourceKind `json:"kind"`
	Name   string             `json:"name,omitempty"`
	Source string             `json:"source"`
}

type directoryContext struct {
	executor    *tools.JobExecutor
	snapshot    harness.Snapshot
	hooks       *toolHookRuntime
	skills      *skillLedger
	tools       *elelem.ToolSet
	prompt      string
	contextHash string
	promptHash  string
}

type directoryScopeOptions struct {
	runtime           *Runtime
	deps              *launchAgentDeps
	rootInput         *TurnRequest
	rootAgent         string
	childInstructions string
	allowedTools      []string
	depth             int
	agentRunID        *uuid.UUID
	initialPrompt     string
	initialHooks      *toolHookRuntime
	skills            *skillLedger
	sessionEvents     elelem.MessageInjector
	contextTokens     hooks.ContextTokenCounter
	apply             func(*directoryContext)
	queue             *activeUserMessageQueue
	warnings          func(context.Context, []harness.Warning) error
}

type directoryScope struct {
	options         directoryScopeOptions
	mutex           sync.RWMutex
	current         *directoryContext
	pending         *directoryContext
	applied         *directoryContext
	gate            directoryGate
	callsMutex      sync.Mutex
	calls           map[string]directoryCall
	results         map[string]string
	completedRounds int
	roundOffset     int
	maximumRounds   int
}

func newDirectoryScope(
	ctx context.Context,
	options directoryScopeOptions,
) (*directoryScope, error) {
	scope := &directoryScope{
		options: options,
		calls:   make(map[string]directoryCall),
		results: make(map[string]string),
	}

	executor := options.deps.executor.ForAgentRun(uuid.Nil)
	if options.agentRunID != nil {
		executor = executor.ForAgentRun(*options.agentRunID)
	}

	current, err := scope.buildContext(
		ctx, executor, options.deps.snapshot, true,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build initial directory scope")
	}

	scope.current = current
	scope.applied = current

	return scope, nil
}

func (s *directoryScope) context() *directoryContext {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.current
}

func (r *Runtime) resolveDirectory(directory string) (harness.Snapshot, error) {
	if resolver, ok := r.resolver.(directoryResolver); ok {
		snapshot, err := resolver.ResolveDirectory(directory)
		if err != nil {
			return harness.Snapshot{}, ctxerrors.Wrap(
				err, "resolve directory harness",
			)
		}

		return snapshot, nil
	}

	snapshot, err := r.resolver.Resolve(directory)
	if err != nil {
		return harness.Snapshot{}, ctxerrors.Wrap(err, "resolve custom harness")
	}

	return snapshot, nil
}

func (s *directoryScope) buildContext(
	ctx context.Context,
	executor *tools.JobExecutor,
	snapshot harness.Snapshot,
	initial bool,
) (*directoryContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "build directory scope")
	}

	skills := newSkillLedger()
	toolHooks := s.options.initialHooks

	prompt := s.options.initialPrompt
	if initial {
		skills = s.options.skills
	} else {
		toolHooks = nil
		prompt = ""
	}

	if prompt == "" {
		var err error

		prompt, err = s.systemPrompt(snapshot, executor.Workspace())
		if err != nil {
			return nil, ctxerrors.Wrap(err, "build directory system prompt")
		}
	}

	if toolHooks == nil {
		var err error

		toolHooks, err = s.newHooks(snapshot, executor)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "build directory hooks")
		}
	}

	registered, err := s.toolSet(snapshot, executor, skills)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build directory tools")
	}

	bindToolHooks(registered, toolHooks)

	return &directoryContext{
		executor: executor, snapshot: snapshot, hooks: toolHooks,
		skills: skills, tools: registered, prompt: prompt,
	}, nil
}

func (s *directoryScope) toolSet(
	snapshot harness.Snapshot,
	executor *tools.JobExecutor,
	skills *skillLedger,
) (*elelem.ToolSet, error) {
	r := s.options.runtime

	allowed := s.options.allowedTools
	if s.options.rootInput != nil {
		definition, err := snapshot.Agent(s.options.rootAgent)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "resolve directory root agent")
		}

		allowed = definition.AllowedTools
	}

	deps := *s.options.deps
	deps.executor = executor
	deps.snapshot = snapshot
	deps.directoryScope = s

	registered, err := r.agentToolSet(
		&deps, skills, s.options.sessionEvents, allowed, s.options.depth,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build directory tools")
	}

	return registered, nil
}

func (s *directoryScope) newHooks(
	snapshot harness.Snapshot,
	executor *tools.JobExecutor,
) (*toolHookRuntime, error) {
	r := s.options.runtime
	deps := s.options.deps

	return newToolHookRuntime(
		snapshot, executor.Workspace(), r.hookStateRoot,
		deps.requestID, deps.sessionID, deps.parentTurnID,
		s.options.agentRunID, executor, s.options.sessionEvents,
		r.hookCommandTimeout, r.maxHookCommandOutput,
		r.durableEventPublisher(), s.options.contextTokens,
		modelTokenCounter(deps.model),
		hooks.Options{
			Callback: r.hookCallback, RunCommand: r.hookCommandRunner,
		},
	)
}

func (s *directoryScope) change(
	ctx context.Context,
	input changeDirectoryInput,
) (changeDirectoryResult, error) {
	current := s.context()

	executor, err := current.executor.AtDirectory(ctx, input.Directory)
	if err != nil {
		return changeDirectoryResult{}, ctxerrors.Wrap(err,
			"validate directory")
	}

	snapshot, err := s.options.runtime.resolveDirectory(executor.Workspace())
	if err != nil {
		return changeDirectoryResult{}, ctxerrors.Wrap(err,
			"load target directory")
	}

	candidate, err := s.buildContext(ctx, executor, snapshot, false)
	if err != nil {
		return changeDirectoryResult{}, ctxerrors.Wrap(err,
			"prepare directory scope")
	}

	r := s.options.runtime

	candidate.contextHash, candidate.promptHash, err = r.saveSnapshots(
		ctx, snapshot, candidate.prompt, candidate.prompt,
	)
	if err != nil {
		return changeDirectoryResult{}, ctxerrors.Wrap(err,
			"save scope snapshots")
	}

	result := harnessSourceChanges(current.snapshot.Manifest(),
		snapshot.Manifest())
	result.directoryMarker = directoryMarker{
		Directory: executor.Workspace(), ContextHash: candidate.contextHash,
		PromptHash: candidate.promptHash,
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if err := ctx.Err(); err != nil {
		return changeDirectoryResult{}, ctxerrors.Wrap(err,
			"stage directory scope")
	}

	s.pending = candidate

	return result, nil
}

// harnessSourceChanges compares the harness before and after a hop, keyed
// by kind, name and source, and reports the sources it added, removed, and
// whose content changed.
func harnessSourceChanges(
	before, after []harness.ManifestEntry,
) changeDirectoryResult {
	sourceOf := func(entry harness.ManifestEntry) harnessSource {
		return harnessSource{
			Kind: entry.Kind, Name: entry.Name, Source: entry.Source,
		}
	}

	previous := make(map[harnessSource]string, len(before))
	for _, entry := range before {
		previous[sourceOf(entry)] = entry.Hash
	}

	var result changeDirectoryResult

	for _, entry := range after {
		source := sourceOf(entry)

		hash, found := previous[source]
		delete(previous, source)

		switch {
		case !found:
			result.Loaded = append(result.Loaded, source)
		case hash != entry.Hash:
			result.Changed = append(result.Changed, source)
		}
	}

	for _, entry := range before {
		if _, removed := previous[sourceOf(entry)]; removed {
			result.Unloaded = append(result.Unloaded, sourceOf(entry))
		}
	}

	return result
}

func (s *directoryScope) systemPrompt(
	snapshot harness.Snapshot,
	directory string,
) (string, error) {
	r := s.options.runtime
	if s.options.rootInput != nil {
		return r.systemPromptForAgent(snapshot, *s.options.rootInput,
			directory, s.options.rootAgent)
	}

	return r.childSystemPrompt(snapshot, s.options.childInstructions, directory)
}

func (r *Runtime) rootDirectoryScope(
	ctx context.Context,
	prepared *preparedTurn,
) (*directoryScope, error) {
	rootAgent := prepared.rootAgent
	if rootAgent == "" {
		rootAgent = r.rootAgent
	}

	return newDirectoryScope(ctx, directoryScopeOptions{
		runtime:       r,
		deps:          r.launchAgentDeps(prepared),
		rootInput:     &prepared.input,
		rootAgent:     rootAgent,
		initialPrompt: prepared.prompt.SystemMessage(),
		initialHooks:  prepared.toolHooks,
		skills:        prepared.skillLedger,
		sessionEvents: prepared.injectSessionEvents,
		contextTokens: prepared.hookContextTokens,
		warnings:      prepared.emitWarnings,
		queue:         prepared.userMessages,
		apply: func(current *directoryContext) {
			prepared.executor = current.executor
			prepared.snapshot = current.snapshot
			prepared.workspace = current.executor.Workspace()
			prepared.skillLedger = current.skills
			prepared.toolHooks = current.hooks
			prepared.contextHash = current.contextHash
			prepared.promptHash = current.promptHash

			prepared.prompt = prepared.prompt.WithSystem(current.prompt).
				ResetSystemAppends()
			if prepared.compactor != nil {
				prepared.compactor.options.Workspace = prepared.workspace
				prepared.compactor.onCompacted = current.skills.forgetThrough
			}
		},
	})
}

func (s *directoryScope) publishContext(
	ctx context.Context,
	current *directoryContext,
) error {
	queue := s.options.queue
	if queue != nil {
		queue.mutex.Lock()
		defer queue.mutex.Unlock()
	}

	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "publish directory context")
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.current = current
	if queue != nil {
		queue.snapshot = current.snapshot
		queue.skills = current.skills

		if current.executor != nil {
			queue.workspace = current.executor.Workspace()
		}
	}

	return nil
}

func logHarnessWarnings(ctx context.Context, warnings []harness.Warning) {
	for _, warning := range warnings {
		runtimecontext.Logger(ctx).Warn(
			"invalid optional harness configuration ignored",
			"kind", warning.Kind,
			"source", warning.Source,
			"reason", warning.Reason,
		)
	}
}

func (s *directoryScope) notifyWarnings(
	ctx context.Context,
	warnings []harness.Warning,
) error {
	logHarnessWarnings(ctx, warnings)

	if s.options.warnings == nil {
		return nil
	}

	return s.options.warnings(ctx, warnings)
}

func (s *directoryScope) bindChildCompactor(compactor *compactor) {
	s.options.apply = func(current *directoryContext) {
		if compactor != nil {
			compactor.options.Workspace = current.executor.Workspace()
		}
	}
}

func (s *directoryScope) childCompactionHook(run *AgentRun) compactionHook {
	return func(
		ctx context.Context,
		event harness.HookEvent,
		payload compactionHookPayload,
	) error {
		hook := childCompactionHook(run, s.context().hooks)

		return hook(ctx, event, payload)
	}
}
