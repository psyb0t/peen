package tools

import (
	"errors"
	"io"
	"io/fs"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

func (e *Executor) readRegularFile(name string, maximum int64) (
	[]byte, fs.FileMode, error,
) {
	file, err := e.filesystem.Open(name)
	if err != nil {
		return nil, 0, wrapPathError(err, "open filesystem file")
	}

	info, err := file.Stat()
	if err != nil {
		return nil, 0, errors.Join(err, file.Close())
	}

	if !info.Mode().IsRegular() || info.Size() > maximum {
		cause := ErrNotRegularFile
		if info.Size() > maximum {
			cause = ErrLimitExceeded
		}

		return nil, 0, errors.Join(ctxerrors.Wrap(
			cause,
			"bounded file read",
		), file.Close())
	}

	content, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, 0, ctxerrors.Wrap(err, "read bounded filesystem file")
	}

	if int64(len(content)) > maximum {
		return nil, 0, ctxerrors.Wrap(
			ErrLimitExceeded,
			"file grew beyond read bound",
		)
	}

	return content, info.Mode().Perm(), nil
}

func (e *Executor) writeFileAtomic(
	name string,
	content []byte,
	mode fs.FileMode,
) error {
	if !e.virtual {
		return writeFileAtomic(name, content, mode)
	}

	if err := e.filesystem.WriteFileAtomic(name, content, mode); err != nil {
		return ctxerrors.Wrap(err, "replace filesystem file")
	}

	return nil
}

func (e *Executor) writeNewFileAtomic(
	name string,
	content []byte,
	mode fs.FileMode,
) error {
	if !e.virtual {
		return writeNewFileAtomic(name, content, mode)
	}

	if err := e.filesystem.CreateExclusive(name, content, mode); err != nil {
		return ctxerrors.Wrap(err, "create filesystem file")
	}

	return nil
}

func (e *Executor) walkDir(root string, visit fs.WalkDirFunc) error {
	if err := workspacefs.WalkDir(e.filesystem, root, visit); err != nil {
		return ctxerrors.Wrap(err, "walk filesystem")
	}

	return nil
}
