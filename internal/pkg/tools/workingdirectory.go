package tools

import (
	"context"
	"maps"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

// AtDirectory returns an independent executor at a canonical existing path.
// It retains this agent's observations and leaves the original unchanged.
func (e *Executor) AtDirectory(
	ctx context.Context,
	directory string,
) (*Executor, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "change tool directory")
	}

	if strings.TrimSpace(directory) == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"directory is required",
		)
	}

	path, err := e.resolvePath(directory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve tool directory")
	}

	canonical, err := e.filesystem.RealPath(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "canonicalize tool directory")
	}

	if e.virtual {
		canonical, err = workspacefs.ValidPath(canonical)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "validate virtual tool directory")
		}
	}

	fork, err := NewExecutor(Options{
		Workspace: canonical, Filesystem: e.filesystem, Virtual: e.virtual,
		ProtectedDirectory: e.protectedDirectory,
		Limits:             e.limits, Environment: e.environment,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open tool directory")
	}

	e.observedMutex.RLock()
	fork.observed = maps.Clone(e.observed)
	e.observedMutex.RUnlock()
	fork.mutation = e.mutation

	return fork, nil
}

// AtDirectory keeps supervision session-scoped while changing file paths.
func (e *JobExecutor) AtDirectory(
	ctx context.Context,
	directory string,
) (*JobExecutor, error) {
	fork, err := e.Executor.AtDirectory(ctx, directory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "change job executor directory")
	}

	return &JobExecutor{
		Executor: fork, jobs: e.jobs, turnID: e.turnID,
		workerGenerationID: e.workerGenerationID,
	}, nil
}

// Fork gives a child a private observation ledger at its parent's directory.
func (e *JobExecutor) Fork(ctx context.Context) (*JobExecutor, error) {
	fork, err := e.AtDirectory(ctx, ".")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "fork agent executor")
	}

	fork.observed = map[string]observation{}

	return fork, nil
}
