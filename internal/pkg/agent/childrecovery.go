package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

type recoveredChild struct {
	run *AgentRun
	//nolint:containedctx // Hold Start's cancellation until all inboxes exist.
	ctx    context.Context
	deps   *launchAgentDeps
	launch preparedChildLaunch
	sink   *agentRunSink
	stored *models.AgentRun
}

// Every inbox is registered before any recovered child can send a message.
func (r *Runtime) recoverSessionChildren(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	// Worker children resume through the durable workflow history.
	if r.workflowManaged || r.workerGenerationID != uuid.Nil ||
		r.nativeWorkflowRunner() != nil {
		return nil
	}

	return r.recoverEmbeddedSessionChildren(ctx, sessionID)
}

func (r *Runtime) recoverEmbeddedSessionChildren(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	r.childRecoveryMutex.Lock()
	defer r.childRecoveryMutex.Unlock()

	if r.recoveredSessions[sessionID] {
		return nil
	}

	runs, err := r.recoveryCandidates(ctx, sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "list child recovery candidates")
	}

	children := make([]recoveredChild, 0, len(runs))

	isReady := false
	defer func() {
		r.finishAbortedRecovery(children, isReady)
	}()

	registry, err := r.sessionAgentRuns(sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "open recovered child registry")
	}

	prepared, err := r.prepareRecoveredChildren(ctx, runs, registry)
	if err != nil {
		return ctxerrors.Wrap(err, "prepare recovery batch")
	}

	for _, candidate := range prepared {
		if !hasRecoveredParent(candidate.stored, registry) {
			continue
		}

		//nolint:contextcheck // Candidate context inherits the caller's scope.
		child, err := r.restoreChild(candidate.ctx, candidate, registry)
		if errors.Is(err, commerr.ErrConflict) {
			continue
		}

		if err != nil {
			return ctxerrors.Wrap(err, "restore child conversation")
		}

		children = append(children, child)
	}

	r.recoveredSessions[sessionID] = true
	isReady = true

	for _, child := range children {
		go r.superviseRecoveredChild(child)
	}

	return nil
}

func (r *Runtime) prepareRecoveredChildren(
	ctx context.Context,
	runs []*models.AgentRun,
	registry *AgentRunRegistry,
) ([]recoveredChild, error) {
	registry.mu.RLock()
	running := registry.runningCountLocked()
	registry.mu.RUnlock()

	prepared := make([]recoveredChild, 0, len(runs))
	parents := map[uuid.UUID]bool{}

	for _, stored := range runs {
		if stored.ParentAgentRunID != nil &&
			!parents[*stored.ParentAgentRunID] &&
			!hasRecoveredParent(stored, registry) {
			continue
		}

		if running+len(prepared) >= r.agentLimits.MaxConcurrentRuns {
			return nil, ctxerrors.Wrap(
				ErrTooManyAgentRuns,
				"recovered children exceed the session limit",
			)
		}

		childCtx := ctxscope.Set(ctx,
			ctxscope.Attr("session_id", stored.SessionID.String()),
			ctxscope.Attr("request_id", stored.RequestID.String()),
			ctxscope.Attr("turn_id", stored.ParentTurnID.String()),
			ctxscope.Attr(
				"worker_generation_id", r.workerGenerationID.String(),
			),
		)

		deps, launch, err := r.recoveredChildLaunch(childCtx, stored, registry)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "validate child recovery setup")
		}

		prepared = append(prepared, recoveredChild{
			ctx: childCtx, stored: stored, deps: deps, launch: launch,
		})
		parents[stored.ID] = true
	}

	return prepared, nil
}

func (r *Runtime) finishAbortedRecovery(
	children []recoveredChild,
	isReady bool,
) {
	if isReady {
		return
	}

	for _, child := range children {
		r.endChildAgentRun(
			child.ctx, child.launch.registry, child.sink, child.run, nil,
			ctxerrors.Wrap(commerr.ErrInvalidState, "child recovery aborted"),
		)
	}
}

func hasRecoveredParent(
	stored *models.AgentRun,
	registry *AgentRunRegistry,
) bool {
	if stored.ParentAgentRunID == nil {
		return true
	}

	parent, exists := registry.Get(*stored.ParentAgentRunID)

	return exists && parent.Snapshot().State == AgentRunStateRunning
}

func (r *Runtime) recoveryCandidates(
	ctx context.Context,
	sessionID uuid.UUID,
) ([]*models.AgentRun, error) {
	if _, err := r.store.PrepareAgentRunRecovery(
		ctx, sessionID, r.workerGenerationID,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "settle previous worker children")
	}

	var runs []*models.AgentRun

	for offset := 0; ; offset += session.MaximumPageLimit {
		page, err := r.store.ListAgentRuns(
			ctx,
			sessionID,
			session.ListAgentRunsOptions{
				Limit:  session.MaximumPageLimit,
				Offset: offset,
			},
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read child recovery page")
		}

		for _, run := range page.Items {
			if r.isChildRecoveryCandidate(run) {
				runs = append(runs, run)
			}
		}

		if !page.HasMore {
			sort.Slice(runs, func(i, j int) bool {
				return runs[i].Depth < runs[j].Depth
			})

			return runs, nil
		}
	}
}

func (r *Runtime) isChildRecoveryCandidate(run *models.AgentRun) bool {
	isInterrupted := run.State == models.AgentRunStateInterrupted
	isStale := run.State == models.AgentRunStateRunning &&
		r.workerGenerationID != uuid.Nil &&
		run.WorkerGenerationID != r.workerGenerationID.String()

	return !run.CancelRequested && (isInterrupted || isStale)
}

func (r *Runtime) restoreChild(
	ctx context.Context,
	child recoveredChild,
	registry *AgentRunRegistry,
) (recoveredChild, error) {
	stored := child.stored

	var sink *agentRunSink

	run, runCtx, err := registry.Start(ctx, StartAgentRunInput{
		ID: stored.ID, ParentTurnID: stored.ParentTurnID,
		ParentAgentRunID: stored.ParentAgentRunID,
		ParentToolCallID: stored.ParentToolCallID,
		RequestID:        stored.RequestID,
		Name:             stored.Name,
		Definition:       string(stored.Definition),
		Depth:            int(stored.Depth), StartedAt: stored.StartedAt,
	}, func(run *AgentRun) error {
		var err error

		sink, err = r.registerRecoveredChild(ctx, stored, run)

		return err
	})
	if err != nil {
		return recoveredChild{}, ctxerrors.Wrap(err, "register recovered child")
	}

	runCtx = childAgentContext(
		runCtx,
		run,
		child.launch.definition,
		child.launch.depth,
		stored.Task,
	)

	child.run, child.ctx, child.sink = run, runCtx, sink

	return child, nil
}

func (r *Runtime) registerRecoveredChild(
	ctx context.Context,
	stored *models.AgentRun,
	run *AgentRun,
) (*agentRunSink, error) {
	sink := newAgentRunSink(
		r.store, stored.SessionID, run, r.recoveredChildSink(stored),
		stored.ModelReference,
	)

	inbox, err := newAgentInbox(sink, r.maxQueuedUserMessages)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build recovered inbox")
	}

	run.inbox = inbox
	if err := r.restoreChildInbox(recoveredChild{
		ctx:    ctx,
		run:    run,
		stored: stored,
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "restore child inbox")
	}

	if _, err := r.store.ClaimAgentRun(
		ctx, stored.SessionID, stored.ID, session.ClaimAgentRunInput{
			GenerationID:         r.workerGenerationID,
			PreviousGenerationID: stored.WorkerGenerationID,
			PreviousState:        stored.State,
		},
	); err != nil {
		return nil, ctxerrors.Wrap(err, "claim recovered child")
	}

	return sink, nil
}

func (r *Runtime) recoveredChildSink(stored *models.AgentRun) EventSink {
	observer := r.sessionObserver(stored.SessionID)
	if observer == nil {
		return nil
	}

	return func(event Event) error {
		event.RequestID = stored.RequestID
		observer(event)

		return nil
	}
}

func (r *Runtime) recoveredChildLaunch(
	ctx context.Context,
	stored *models.AgentRun,
	registry *AgentRunRegistry,
) (*launchAgentDeps, preparedChildLaunch, error) {
	definition, err := recoveredChildDefinition(stored, r.agentLimits.MaxDepth)
	if err != nil {
		return nil, preparedChildLaunch{}, ctxerrors.Wrap(
			err, "validate recovered child",
		)
	}

	model, err := r.recoveredChildModel(stored)
	if err != nil {
		return nil, preparedChildLaunch{}, ctxerrors.Wrap(
			err, "resolve recovered child model",
		)
	}

	snapshot, err := r.resolveDirectory(stored.Workspace)
	if err != nil {
		return nil, preparedChildLaunch{}, ctxerrors.Wrap(
			err, "resolve recovered child directory",
		)
	}

	jobExecutor, err := r.recoveredChildExecutor(ctx, stored)
	if err != nil {
		return nil, preparedChildLaunch{}, ctxerrors.Wrap(
			err, "open recovered child executor",
		)
	}

	prompt, err := r.childSystemPrompt(
		snapshot, stored.Instructions, jobExecutor.Workspace(),
	)
	if err != nil {
		return nil, preparedChildLaunch{}, ctxerrors.Wrap(
			err, "build recovered child prompt",
		)
	}

	return &launchAgentDeps{
			runtime:            r,
			executor:           jobExecutor,
			snapshot:           snapshot,
			model:              model,
			modelReference:     stored.ModelReference,
			workerGenerationID: r.workerGenerationID.String(),
			sessionID:          stored.SessionID,
			parentTurnID:       stored.ParentTurnID,
			requestID:          stored.RequestID,
		}, preparedChildLaunch{
			definition:       definition,
			depth:            int(stored.Depth),
			systemPrompt:     prompt,
			allowedToolsJSON: stored.AllowedToolsJSON,
			registry:         registry,
			skillLedger:      newSkillLedger(),
		}, nil
}

func (r *Runtime) recoveredChildModel(
	stored *models.AgentRun,
) (ModelClient, error) {
	model, err := r.models.ResolveModel(stored.ModelReference)
	if err != nil {
		return ModelClient{}, ctxerrors.Wrap(err, "resolve saved model")
	}

	model.Model.ID = stored.ModelID

	return model, nil
}

func recoveredChildDefinition(
	stored *models.AgentRun,
	maximumDepth int,
) (childDefinition, error) {
	var allowedTools []string
	if err := json.Unmarshal(
		[]byte(stored.AllowedToolsJSON),
		&allowedTools,
	); err != nil {
		return childDefinition{}, ctxerrors.Wrap(
			err, "decode child tool ceiling",
		)
	}

	if stored.Depth < 1 || stored.Depth > int64(maximumDepth) {
		return childDefinition{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"recovered child depth",
		)
	}

	return childDefinition{
		name: stored.Name, instructions: stored.Instructions,
		allowedTools: allowedTools, kind: string(stored.Definition),
	}, nil
}

func (r *Runtime) recoveredChildExecutor(
	ctx context.Context,
	stored *models.AgentRun,
) (*tools.JobExecutor, error) {
	storedSession, err := r.store.Get(ctx, stored.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get recovered child session")
	}

	jobs, err := r.sessionJobs(stored.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open recovered jobs")
	}

	executor, err := tools.NewExecutor(tools.Options{
		Workspace:          stored.Workspace,
		ProtectedDirectory: r.toolWorkspace(storedSession.Workspace),
		Filesystem:         r.filesystem,
		Virtual:            r.virtualFilesystem,
		Limits:             r.toolLimits,
		Environment:        r.commandEnvironment,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open recovered file executor")
	}

	jobExecutor, err := tools.NewJobExecutor(
		executor,
		jobs,
		stored.ParentTurnID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open recovered child executor")
	}

	return jobExecutor, nil
}

func (r *Runtime) superviseRecoveredChild(child recoveredChild) {
	defer func() {
		if recovered := recover(); recovered != nil {
			runtimecontext.Logger(child.ctx).Error("child recovery panicked")

			if child.run.Snapshot().State != AgentRunStateRunning {
				return
			}

			r.endChildAgentRun(
				child.ctx,
				child.launch.registry,
				child.sink,
				child.run,
				nil,
				ctxerrors.Wrap(
					commerr.ErrInvalidState,
					"child recovery panicked",
				),
			)
		}
	}()

	budget, err := r.childRoundsUsed(
		child.ctx, child.stored.SessionID, child.run.ID,
	)
	if err != nil {
		r.endChildAgentRun(
			child.ctx, child.launch.registry, child.sink, child.run, nil, err,
		)

		return
	}

	child.launch.completedRounds = budget.used
	child.launch.maximumRounds = budget.maximum

	prompt, ledger, err := r.recoverChildPrompt(child)
	if err != nil {
		r.endChildAgentRun(
			child.ctx,
			child.launch.registry,
			child.sink,
			child.run,
			nil,
			err,
		)

		return
	}

	child.launch.recoveredPrompt = &prompt
	child.launch.skillLedger = ledger
	r.superviseChildAgent(
		child.ctx,
		child.deps,
		child.launch,
		child.run,
		child.sink,
		child.stored.Task,
	)
}
