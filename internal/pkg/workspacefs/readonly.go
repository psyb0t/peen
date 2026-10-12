package workspacefs

import (
	"io/fs"

	"github.com/psyb0t/ctxerrors"
)

// ReadOnly adapts an io/fs filesystem, enforcing relative valid paths.
type ReadOnly struct{ Source fs.FS }

func (r ReadOnly) Stat(name string) (fs.FileInfo, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	info, err := fs.Stat(r.Source, name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat read-only path")
	}

	return info, nil
}

func (r ReadOnly) Lstat(name string) (fs.FileInfo, error) {
	return r.Stat(name)
}

func (r ReadOnly) Open(name string) (fs.File, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	file, err := r.Source.Open(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open read-only path")
	}

	return file, nil
}

func (r ReadOnly) ReadDir(name string) ([]fs.DirEntry, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	entries, err := fs.ReadDir(r.Source, name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read read-only directory")
	}

	return entries, nil
}

func (r ReadOnly) ReadFile(name string) ([]byte, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	data, err := fs.ReadFile(r.Source, name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read read-only file")
	}

	return data, nil
}

func (r ReadOnly) RealPath(name string) (string, error) {
	return ValidPath(name)
}

func (r ReadOnly) CreateExclusive(
	string,
	[]byte,
	fs.FileMode,
) error {
	return readOnlyError()
}

func (r ReadOnly) WriteFileAtomic(
	string,
	[]byte,
	fs.FileMode,
) error {
	return readOnlyError()
}

func (r ReadOnly) MkdirAll(
	string,
	fs.FileMode,
) error {
	return readOnlyError()
}

func (r ReadOnly) Remove(
	string,
) error {
	return readOnlyError()
}

func (r ReadOnly) RemoveAll(
	string,
) error {
	return readOnlyError()
}

func (r ReadOnly) RenameNoReplace(
	string,
	string,
) error {
	return readOnlyError()
}

func (r ReadOnly) Chmod(
	string,
	fs.FileMode,
) error {
	return readOnlyError()
}

func readOnlyError() error {
	return ctxerrors.Wrap(
		fs.ErrPermission,
		"read-only filesystem",
	)
}
