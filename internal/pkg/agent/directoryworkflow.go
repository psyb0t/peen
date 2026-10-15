package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

type workflowPendingDirectory struct {
	Runtime *workflowRuntimeState `json:"runtime"`
	Prompt  string                `json:"prompt"`
}

func (s *directoryScope) workflowPendingDirectory() (
	*workflowPendingDirectory, error,
) {
	s.mutex.RLock()
	pending := s.pending
	s.mutex.RUnlock()

	if pending == nil {
		return nil, nil //nolint:nilnil // No pending hop is valid.
	}

	state, err := snapshotWorkflowRuntime(pending)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "snapshot pending directory")
	}

	return &workflowPendingDirectory{
		Runtime: state, Prompt: pending.prompt,
	}, nil
}

func (s *directoryScope) restoreWorkflowPendingDirectory(
	ctx context.Context,
	saved *workflowPendingDirectory,
) error {
	var candidate *directoryContext

	if saved != nil {
		var err error

		candidate, err = s.restorePendingDirectoryContext(ctx, saved)
		if err != nil {
			return ctxerrors.Wrap(err, "restore pending directory context")
		}
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "replace pending directory context")
	}

	s.pending = candidate

	return nil
}

func (s *directoryScope) restorePendingDirectoryContext(
	ctx context.Context,
	saved *workflowPendingDirectory,
) (*directoryContext, error) {
	candidate, err := s.restorePendingDirectoryState(ctx, saved)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending directory state")
	}

	hooks, err := s.newHooks(candidate.snapshot, candidate.executor)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending hooks")
	}

	if err := hooks.restoreWorkflowState(saved.Runtime.Hooks); err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending hook state")
	}

	registered, err := s.toolSet(candidate.snapshot, candidate.executor,
		candidate.skills)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending tool bindings")
	}

	bindToolHooks(registered, hooks)
	candidate.hooks, candidate.tools = hooks, registered

	return candidate, nil
}

func (s *directoryScope) restorePendingDirectoryState(
	ctx context.Context,
	saved *workflowPendingDirectory,
) (*directoryContext, error) {
	if saved.Runtime == nil || saved.Prompt == "" {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"pending directory checkpoint")
	}

	state := saved.Runtime

	snapshot, err := harness.RestoreCheckpoint(state.Harness)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending harness")
	}

	if snapshot.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"pending harness and directory differ")
	}

	executor, err := s.context().executor.AtCanonicalDirectory(ctx,
		state.Directory.Directory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending executor")
	}

	if executor.Workspace() != state.Directory.Directory {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"pending directory is not canonical")
	}

	if err := executor.RestoreObservations(state.Observations); err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending read observations")
	}

	skills, err := restoreWorkflowSkills(state.Skills)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending skills")
	}

	return &directoryContext{
		executor: executor, snapshot: snapshot, skills: skills,
		prompt:      saved.Prompt,
		contextHash: state.Directory.ContextHash,
		promptHash:  state.Directory.PromptHash,
	}, nil
}
