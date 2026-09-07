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

// WriteFile creates a new file or replaces an existing one. Creation at a
// path proven absent requires no ExpectedSHA256 and rejects one if supplied.
// Replacing an existing file requires the path to have been observed this
// turn and requires ExpectedSHA256 to match the file's current content, so a
// concurrent change is never silently overwritten. Existing permission bits
// are preserved on replacement.
func (e *Executor) WriteFile(
	ctx context.Context,
	input WriteFileInput,
) (WriteFileOutput, error) {
	if err := ctx.Err(); err != nil {
		return WriteFileOutput{}, ctxerrors.Wrap(err, "write file canceled")
	}

	if strings.TrimSpace(input.Path) == "" {
		return WriteFileOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"write path is required",
		)
	}

	content := []byte(input.Content)
	if len(content) > e.Limits().MaxWriteBytes {
		return WriteFileOutput{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"content exceeds write bound",
		)
	}

	path, err := e.resolvePath(input.Path)
	if err != nil {
		return WriteFileOutput{}, ctxerrors.Wrap(err, "resolve write path")
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	return e.writeFileLocked(path, input, content)
}

func (e *Executor) writeFileLocked(
	path string,
	input WriteFileInput,
	content []byte,
) (WriteFileOutput, error) {
	// Stat, not Lstat: a symlink to a regular file is a regular file to write
	// to. writeFileAtomic resolves the link so the replacement lands on the
	// target instead of clobbering the link.
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return e.createFile(path, input, content)
		}

		return WriteFileOutput{}, wrapPathError(err, "stat write path")
	}

	if !info.Mode().IsRegular() {
		return WriteFileOutput{}, ctxerrors.Wrap(ErrNotRegularFile, path)
	}

	return e.replaceFile(path, input, content)
}

// createFile writes content at a path proven absent by the caller's stat.
func (e *Executor) createFile(
	path string,
	input WriteFileInput,
	content []byte,
) (WriteFileOutput, error) {
	if input.ExpectedSHA256 != "" {
		return WriteFileOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"expected hash on a path that does not exist",
		)
	}

	if err := os.MkdirAll(filepath.Dir(path), newDirectoryMode); err != nil {
		return WriteFileOutput{}, wrapPathError(err, "create parent directory")
	}

	if err := writeNewFileAtomic(path, content, newFileMode); err != nil {
		return WriteFileOutput{}, ctxerrors.Wrap(err, "write new file")
	}

	return e.finishWrite(path, content, true)
}

// replaceFile overwrites an existing regular file after checking the caller
// observed it and supplied a hash matching its current content.
func (e *Executor) replaceFile(
	path string,
	input WriteFileInput,
	content []byte,
) (WriteFileOutput, error) {
	if input.ExpectedSHA256 == "" {
		return WriteFileOutput{}, ctxerrors.Wrap(ErrHashRequired, path)
	}

	current, mode, err := readRegularFile(path, int64(e.Limits().MaxWriteBytes))
	if err != nil {
		return WriteFileOutput{}, err
	}

	if err := e.requireObservedContent(path, current); err != nil {
		return WriteFileOutput{}, err
	}

	if hashBytes(current) != input.ExpectedSHA256 {
		return WriteFileOutput{}, ctxerrors.Wrap(ErrStaleHash, path)
	}

	if err := writeFileAtomic(path, content, mode); err != nil {
		return WriteFileOutput{}, ctxerrors.Wrap(err, "replace file")
	}

	return e.finishWrite(path, content, false)
}

// finishWrite records the new content hash as observed so a follow-up edit
// in the same turn sees the write it just made.
func (e *Executor) finishWrite(
	path string,
	content []byte,
	created bool,
) (WriteFileOutput, error) {
	newHash := hashBytes(content)
	e.observeContent(path, newHash)

	return WriteFileOutput{
		Path:    path,
		Created: created,
		Bytes:   len(content),
		SHA256:  newHash,
	}, nil
}
