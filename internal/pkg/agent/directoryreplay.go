package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

type directoryResult struct {
	directory string
	sequence  int64
}

// currentDirectory recovers only paired, successful changeDirectory results.
// Its store query includes compacted and interrupted transcript records.
func (r *Runtime) currentDirectory(
	ctx context.Context,
	sessionID uuid.UUID,
	workspace string,
) (string, error) {
	result, err := r.currentDirectoryRecord(ctx, sessionID, workspace)
	if err != nil {
		return "", ctxerrors.Wrap(err, "recover directory record")
	}

	return result.directory, nil
}

func (r *Runtime) currentDirectoryRecord(
	ctx context.Context,
	sessionID uuid.UUID,
	workspace string,
) (directoryResult, error) {
	state, err := r.store.GetDirectoryState(ctx, sessionID)
	if err != nil {
		return directoryResult{}, ctxerrors.Wrap(err, "recover directory")
	}

	directory := state.Directory
	if directory == "" {
		directory = r.toolWorkspace(workspace)
	}

	return directoryResult{directory: directory, sequence: state.Sequence}, nil
}
