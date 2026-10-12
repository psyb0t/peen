package agent

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const virtualWorkspaceRoot = "."

func (r *Runtime) toolWorkspace(workspace string) string {
	if r.virtualFilesystem {
		return virtualWorkspaceRoot
	}

	return workspace
}

// ResolveHarness returns a fresh snapshot without running a provider call.
func (r *Runtime) ResolveHarness(workspace string) (harness.Snapshot, error) {
	value, err := r.resolver.Resolve(workspace)
	if err != nil {
		return harness.Snapshot{}, ctxerrors.Wrap(
			err,
			"resolve embedding harness",
		)
	}

	return value, nil
}
