package workspacefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"golang.org/x/sys/unix"
)

const temporaryPrefix = ".peen-fs-"

// Host preserves the process account's full host filesystem access.
type Host struct{ Directory string }

func (h Host) resolve(name string) string {
	if filepath.IsAbs(name) {
		return name
	}

	return filepath.Join(h.Directory, name)
}

func (h Host) Stat(name string) (fs.FileInfo, error) {
	info, err := os.Stat(h.resolve(name))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat host path")
	}

	return info, nil
}

func (h Host) Lstat(name string) (fs.FileInfo, error) {
	info, err := os.Lstat(h.resolve(name))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "inspect host path")
	}

	return info, nil
}

func (h Host) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(h.resolve(name))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read host directory")
	}

	return entries, nil
}

func (h Host) Open(name string) (fs.File, error) {
	file, err := os.Open(h.resolve(name))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open host file")
	}

	return file, nil
}

func (h Host) ReadFile(name string) ([]byte, error) {
	content, err := os.ReadFile(h.resolve(name))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read host file")
	}

	return content, nil
}

func (h Host) RealPath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(h.resolve(name))
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve host symlinks")
	}

	return resolved, nil
}

func (h Host) MkdirAll(name string, mode fs.FileMode) error {
	if err := os.MkdirAll(h.resolve(name), mode); err != nil {
		return ctxerrors.Wrap(err, "create host directory")
	}

	return nil
}

func (h Host) Remove(name string) error {
	if err := os.Remove(h.resolve(name)); err != nil {
		return ctxerrors.Wrap(err, "remove host path")
	}

	return nil
}

func (h Host) RemoveAll(name string) error {
	if err := os.RemoveAll(h.resolve(name)); err != nil {
		return ctxerrors.Wrap(err, "remove host tree")
	}

	return nil
}

func (h Host) Chmod(name string, mode fs.FileMode) error {
	if err := os.Chmod(h.resolve(name), mode); err != nil {
		return ctxerrors.Wrap(err, "chmod host path")
	}

	return nil
}

func (h Host) RenameNoReplace(source, destination string) error {
	if err := unix.Renameat2(
		unix.AT_FDCWD, h.resolve(source),
		unix.AT_FDCWD, h.resolve(destination),
		unix.RENAME_NOREPLACE,
	); err != nil {
		return ctxerrors.Wrap(err, "rename host path exclusively")
	}

	return nil
}

func (h Host) CreateExclusive(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	return h.writeAtomic(name, data, mode, true)
}

func (h Host) WriteFileAtomic(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	return h.writeAtomic(name, data, mode, false)
}

func (h Host) writeAtomic(
	name string,
	data []byte,
	mode fs.FileMode,
	exclusive bool,
) error {
	target := h.resolve(name)
	if !exclusive {
		resolved, err := h.RealPath(name)
		if err == nil {
			target = resolved
		}

		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	temporary := filepath.Join(
		filepath.Dir(target),
		temporaryPrefix+uuid.NewString(),
	)

	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return ctxerrors.Wrap(err, "create atomic host temporary")
	}

	writeErr := finishFile(file, data, mode)
	if writeErr == nil {
		if exclusive {
			writeErr = h.RenameNoReplace(temporary, target)
		} else {
			writeErr = os.Rename(temporary, target)
		}
	}

	if writeErr != nil {
		return errors.Join(ctxerrors.Wrap(writeErr, "publish atomic host file"),
			removeIfPresent(temporary))
	}

	return nil
}

func finishFile(file *os.File, data []byte, mode fs.FileMode) error {
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}

	if writeErr == nil {
		writeErr = file.Sync()
	}

	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return ctxerrors.Wrap(err, "finish atomic file")
	}

	return nil
}

func removeIfPresent(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ctxerrors.Wrap(err, "remove atomic temporary")
	}

	return nil
}
