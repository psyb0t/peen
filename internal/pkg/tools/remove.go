package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// removedOne is the count reported for a single-node removal: one
	// regular file, one symlink, or one already-empty directory.
	removedOne = 1
	rootPath   = "/"
)

// RemovePath removes a file, symlink, or directory. A regular file requires
// a matching ExpectedSHA256, a symlink is removed as itself and never
// follows its target, and a non-empty directory requires Recursive and is
// bounded by MaxRemoveEntries.
func (e *Executor) RemovePath(
	ctx context.Context,
	input RemovePathInput,
) (RemovePathOutput, error) {
	if err := ctx.Err(); err != nil {
		return RemovePathOutput{}, ctxerrors.Wrap(err, "remove path canceled")
	}

	if strings.TrimSpace(input.Path) == "" {
		return RemovePathOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"remove path is required",
		)
	}

	path, err := e.resolvePath(input.Path)
	if err != nil {
		return RemovePathOutput{}, ctxerrors.Wrap(err, "resolve remove path")
	}

	if path == e.Workspace() || path == rootPath {
		return RemovePathOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"refusing to remove the workspace root or filesystem root",
		)
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	return e.removePathLocked(path, input)
}

func (e *Executor) removePathLocked(
	path string,
	input RemovePathInput,
) (RemovePathOutput, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RemovePathOutput{}, wrapPathError(err, "stat remove path")
	}

	if err := e.requireObserved(path); err != nil {
		return RemovePathOutput{}, err
	}

	switch entryTypeOf(info.Mode()) {
	case EntryTypeDirectory:
		return e.removeDirectory(path, input)
	case EntryTypeFile:
		return e.removeFile(path, input)
	case EntryTypeSymlink, EntryTypeOther:
		return removeLink(path)
	}

	return RemovePathOutput{}, ctxerrors.Wrap(ErrNotRegularFile, path)
}

// removeFile removes a regular file after checking its current content
// hashes to ExpectedSHA256.
func (e *Executor) removeFile(
	path string,
	input RemovePathInput,
) (RemovePathOutput, error) {
	if input.ExpectedSHA256 == "" {
		return RemovePathOutput{}, ctxerrors.Wrap(ErrHashRequired, path)
	}

	content, _, err := readRegularFile(path, int64(e.Limits().MaxWriteBytes))
	if err != nil {
		return RemovePathOutput{}, err
	}

	if err := e.requireObservedContent(path, content); err != nil {
		return RemovePathOutput{}, err
	}

	if hashBytes(content) != input.ExpectedSHA256 {
		return RemovePathOutput{}, ctxerrors.Wrap(ErrStaleHash, path)
	}

	if err := os.Remove(path); err != nil {
		return RemovePathOutput{}, wrapPathError(err, "remove file")
	}

	return RemovePathOutput{Path: path, Removed: removedOne}, nil
}

// removeLink removes a symlink or other non-regular node as itself, never
// following it, and requires no content hash.
func removeLink(path string) (RemovePathOutput, error) {
	if err := os.Remove(path); err != nil {
		return RemovePathOutput{}, wrapPathError(err, "remove link")
	}

	return RemovePathOutput{Path: path, Removed: removedOne}, nil
}

// removeDirectory removes an empty directory outright, or a non-empty one
// only when Recursive is set and its entry count fits MaxRemoveEntries.
func (e *Executor) removeDirectory(
	path string,
	input RemovePathInput,
) (RemovePathOutput, error) {
	if input.ExpectedSHA256 != "" {
		return RemovePathOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"directory removal does not take a content hash",
		)
	}

	if !input.Recursive {
		return removeEmptyDirectory(path)
	}

	return e.removeDirectoryTree(path)
}

func removeEmptyDirectory(path string) (RemovePathOutput, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return RemovePathOutput{}, wrapPathError(err, "read directory")
	}

	if len(entries) > 0 {
		return RemovePathOutput{}, ctxerrors.Wrap(ErrDirectoryNotEmpty, path)
	}

	if err := os.Remove(path); err != nil {
		return RemovePathOutput{}, wrapPathError(err, "remove empty directory")
	}

	return RemovePathOutput{Path: path, Removed: removedOne}, nil
}

func (e *Executor) removeDirectoryTree(path string) (RemovePathOutput, error) {
	total, err := countTreeEntries(path)
	if err != nil {
		return RemovePathOutput{}, err
	}

	if total > e.Limits().MaxRemoveEntries {
		return RemovePathOutput{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"too many entries to remove",
		)
	}

	if err := os.RemoveAll(path); err != nil {
		return RemovePathOutput{}, wrapPathError(err, "remove directory tree")
	}

	return RemovePathOutput{Path: path, Removed: total}, nil
}

// countTreeEntries counts every filesystem node under root, root included,
// so the reported total matches what a subsequent RemoveAll deletes.
func countTreeEntries(root string) (int, error) {
	count := 0

	err := filepath.WalkDir(
		root,
		func(_ string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			count++

			return nil
		},
	)
	if err != nil {
		return 0, wrapPathError(err, "walk directory tree")
	}

	return count, nil
}
