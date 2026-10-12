package peen

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

// FS is the caller-owned workspace filesystem. Virtual paths are relative
// and slash-separated. Atomic mutations must preserve complete contents and
// exclusive operations must never overwrite an existing destination.
type FS interface {
	FSReader
	FSWriter
}

// FSReader supplies metadata, bounded reads and canonical path identity.
type FSReader interface {
	Stat(string) (fs.FileInfo, error)
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Open(string) (fs.File, error)
	ReadFile(string) ([]byte, error)
	RealPath(string) (string, error)
}

// FSWriter preserves atomic and exclusive mutation guarantees.
type FSWriter interface {
	CreateExclusive(string, []byte, fs.FileMode) error
	WriteFileAtomic(string, []byte, fs.FileMode) error
	MkdirAll(string, fs.FileMode) error
	Remove(string) error
	RemoveAll(string) error
	RenameNoReplace(string, string) error
	Chmod(string, fs.FileMode) error
}

type filesystemAdapter struct{ backing workspacefs.FS }

// HostFS preserves the host account's access. It is not a directory jail.
type HostFS struct {
	filesystemAdapter
	directory string
}

// RootedFS owns an os.Root jail. Close it after all runtimes using it close.
type RootedFS struct {
	filesystemAdapter
	root *os.Root
}

// ReadOnlyFS rejects mutations; the underlying source owns symlink behavior.
type ReadOnlyFS struct{ filesystemAdapter }

// OSFS selects host access and ancestor harness discovery above dir.
func OSFS(dir string) (*HostFS, error) {
	directory, err := filepath.Abs(dir)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve host filesystem")
	}

	info, err := os.Stat(directory)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat host filesystem")
	}

	if !info.IsDir() {
		return nil, ctxerrors.Wrap(fs.ErrInvalid, "host directory")
	}

	return &HostFS{filesystemAdapter: filesystemAdapter{
		backing: workspacefs.Host{Directory: directory},
	}, directory: directory}, nil
}

// RootFS confines operations through os.Root, including symlink traversal.
func RootFS(dir string) (*RootedFS, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open rooted filesystem")
	}

	return &RootedFS{filesystemAdapter: filesystemAdapter{
		backing: workspacefs.Root{Handle: root},
	}, root: root}, nil
}

func (r *RootedFS) Close() error {
	if err := r.root.Close(); err != nil {
		return ctxerrors.Wrap(err, "close rooted filesystem")
	}

	return nil
}

// FromIOFS adapts an io/fs tree. os.DirFS alone is not a symlink jail.
// Nil interfaces and source pointers return fs.ErrInvalid.
func FromIOFS(source fs.FS) (*ReadOnlyFS, error) {
	if source == nil || isNilPointer(source) {
		return nil, ctxerrors.Wrap(fs.ErrInvalid, "nil filesystem")
	}

	return &ReadOnlyFS{filesystemAdapter{
		backing: workspacefs.ReadOnly{Source: source},
	}}, nil
}

func (a filesystemAdapter) Stat(
	name string,
) (fs.FileInfo, error) {
	value, err := a.backing.Stat(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat workspace file")
	}

	return value, nil
}

func (a filesystemAdapter) Lstat(
	name string,
) (fs.FileInfo, error) {
	value, err := a.backing.Lstat(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "inspect workspace path")
	}

	return value, nil
}

func (a filesystemAdapter) ReadDir(
	name string,
) ([]fs.DirEntry, error) {
	value, err := a.backing.ReadDir(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workspace directory")
	}

	return value, nil
}

func (a filesystemAdapter) Open(
	name string,
) (fs.File, error) {
	value, err := a.backing.Open(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open workspace file")
	}

	return value, nil
}

func (a filesystemAdapter) ReadFile(
	name string,
) ([]byte, error) {
	value, err := a.backing.ReadFile(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workspace file")
	}

	return value, nil
}

func (a filesystemAdapter) RealPath(
	name string,
) (string, error) {
	value, err := a.backing.RealPath(name)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve workspace path")
	}

	return value, nil
}

func (a filesystemAdapter) CreateExclusive(
	name string, data []byte, mode fs.FileMode,
) error {
	if err := a.backing.CreateExclusive(name, data, mode); err != nil {
		return ctxerrors.Wrap(err, "create workspace file exclusively")
	}

	return nil
}

func (a filesystemAdapter) WriteFileAtomic(
	name string, data []byte, mode fs.FileMode,
) error {
	if err := a.backing.WriteFileAtomic(name, data, mode); err != nil {
		return ctxerrors.Wrap(err, "replace workspace file")
	}

	return nil
}

func (a filesystemAdapter) MkdirAll(
	name string, mode fs.FileMode,
) error {
	if err := a.backing.MkdirAll(name, mode); err != nil {
		return ctxerrors.Wrap(err, "create workspace directory")
	}

	return nil
}

func (a filesystemAdapter) Remove(
	name string,
) error {
	if err := a.backing.Remove(name); err != nil {
		return ctxerrors.Wrap(err, "remove workspace path")
	}

	return nil
}

func (a filesystemAdapter) RemoveAll(
	name string,
) error {
	if err := a.backing.RemoveAll(name); err != nil {
		return ctxerrors.Wrap(err, "remove workspace tree")
	}

	return nil
}

func (a filesystemAdapter) RenameNoReplace(
	source, destination string,
) error {
	if err := a.backing.RenameNoReplace(source, destination); err != nil {
		return ctxerrors.Wrap(err, "rename workspace path exclusively")
	}

	return nil
}

func (a filesystemAdapter) Chmod(
	name string, mode fs.FileMode,
) error {
	if err := a.backing.Chmod(name, mode); err != nil {
		return ctxerrors.Wrap(err, "change workspace mode")
	}

	return nil
}
