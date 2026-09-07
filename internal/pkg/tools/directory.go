package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// MakeDirectory creates a directory tree, creating any missing ancestors.
// An already-existing directory succeeds with an empty Created list.
func (e *Executor) MakeDirectory(
	ctx context.Context,
	input MakeDirectoryInput,
) (MakeDirectoryOutput, error) {
	if err := ctx.Err(); err != nil {
		return MakeDirectoryOutput{}, ctxerrors.Wrap(
			err,
			"make directory canceled",
		)
	}

	if strings.TrimSpace(input.Path) == "" {
		return MakeDirectoryOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"directory path is required",
		)
	}

	path, err := e.resolvePath(input.Path)
	if err != nil {
		return MakeDirectoryOutput{}, ctxerrors.Wrap(
			err,
			"resolve directory path",
		)
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	return e.makeDirectoryLocked(path)
}

func (e *Executor) makeDirectoryLocked(
	path string,
) (MakeDirectoryOutput, error) {
	created, err := e.ensureDirectory(path)
	if err != nil {
		return MakeDirectoryOutput{}, err
	}

	e.observe(path)

	return MakeDirectoryOutput{Path: path, Created: created}, nil
}

// ensureDirectory makes path a directory, reporting the ancestors it
// actually created, deepest last. An existing directory reports none.
func (e *Executor) ensureDirectory(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return nil, ctxerrors.Wrap(ErrNotDirectory, path)
		}

		return nil, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, wrapPathError(err, "stat directory path")
	}

	missing := missingAncestors(path)
	if len(missing) > e.Limits().MaxRemoveEntries {
		return nil, ctxerrors.Wrap(
			ErrLimitExceeded,
			"too many directories to create",
		)
	}

	if err := os.MkdirAll(path, newDirectoryMode); err != nil {
		return nil, wrapPathError(err, "create directory")
	}

	return missing, nil
}

// missingAncestors walks upward from path collecting the directories that do
// not exist yet, ordered shallowest first so the deepest, the target itself,
// comes last.
func missingAncestors(path string) []string {
	var missing []string

	for current := path; ; {
		_, statErr := os.Stat(current)
		if statErr == nil {
			break
		}

		if !errors.Is(statErr, fs.ErrNotExist) {
			break
		}

		missing = append(missing, current)

		parent := filepath.Dir(current)
		if parent == current {
			break
		}

		current = parent
	}

	reverseInPlace(missing)

	return missing
}

func reverseInPlace(values []string) {
	for left, right := 0, len(values)-1; left < right; {
		values[left], values[right] = values[right], values[left]
		left++
		right--
	}
}
