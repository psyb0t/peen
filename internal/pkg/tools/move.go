package tools

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// MovePath renames a file or directory. The source must have been observed
// this turn and the destination must not already exist; MovePath never
// overwrites.
func (e *Executor) MovePath(
	ctx context.Context,
	input MovePathInput,
) (MovePathOutput, error) {
	if err := ctx.Err(); err != nil {
		return MovePathOutput{}, ctxerrors.Wrap(err, "move path canceled")
	}

	if strings.TrimSpace(input.Source) == "" ||
		strings.TrimSpace(input.Destination) == "" {
		return MovePathOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"source and destination are required",
		)
	}

	source, err := e.resolvePath(input.Source)
	if err != nil {
		return MovePathOutput{}, ctxerrors.Wrap(err, "resolve move source")
	}

	destination, err := e.resolvePath(input.Destination)
	if err != nil {
		return MovePathOutput{}, ctxerrors.Wrap(err, "resolve move destination")
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	return e.movePathLocked(source, destination)
}

func (e *Executor) movePathLocked(
	source, destination string,
) (MovePathOutput, error) {
	info, err := e.filesystem.Lstat(source)
	if err != nil {
		return MovePathOutput{}, wrapPathError(err, "stat move source")
	}

	if err := e.requireMoveObservation(source, info); err != nil {
		return MovePathOutput{}, err
	}

	if err := e.checkDestinationFree(destination); err != nil {
		return MovePathOutput{}, err
	}

	parent := filepath.Dir(destination)
	if err := e.filesystem.MkdirAll(parent, newDirectoryMode); err != nil {
		return MovePathOutput{}, wrapPathError(err, "create destination parent")
	}

	if err := e.filesystem.RenameNoReplace(source, destination); err != nil {
		return MovePathOutput{}, wrapPathError(err, "rename path")
	}

	e.observe(destination)

	return MovePathOutput{Source: source, Destination: destination}, nil
}

func (e *Executor) requireMoveObservation(
	source string,
	info fs.FileInfo,
) error {
	if !info.Mode().IsRegular() {
		return e.requireObserved(source)
	}

	content, _, err := e.readRegularFile(
		source,
		int64(e.limits.MaxWriteBytes),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read file before moving it")
	}

	return e.requireObservedContent(source, content)
}

// checkDestinationFree rejects a move whose destination already exists,
// using Lstat so a destination that is itself a dangling symlink still
// counts as occupied.
func (e *Executor) checkDestinationFree(destination string) error {
	_, err := e.filesystem.Lstat(destination)
	if err == nil {
		return ctxerrors.Wrap(commerr.ErrAlreadyExists, "move destination")
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return wrapPathError(err, "stat move destination")
	}

	return nil
}
