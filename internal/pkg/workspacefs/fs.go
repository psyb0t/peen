// Package workspacefs supplies the filesystem boundary shared by tools and
// harness discovery. Storage and hook state remain runtime-owned host files.
package workspacefs

import (
	"errors"
	"io/fs"
	"path"
	"sort"

	"github.com/psyb0t/ctxerrors"
)

// Reader is the filesystem surface needed by prompts and harness discovery.
type Reader interface {
	Stat(string) (fs.FileInfo, error)
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Open(string) (fs.File, error)
	ReadFile(string) ([]byte, error)
	RealPath(string) (string, error)
}

// FS additionally supplies atomic mutations used by file tools.
type FS interface {
	Reader
	CreateExclusive(string, []byte, fs.FileMode) error
	WriteFileAtomic(string, []byte, fs.FileMode) error
	MkdirAll(string, fs.FileMode) error
	Remove(string) error
	RemoveAll(string) error
	RenameNoReplace(string, string) error
	Chmod(string, fs.FileMode) error
}

// ValidPath rejects absolute paths and traversal beyond the virtual root.
func ValidPath(name string) (string, error) {
	if name == "" {
		return ".", nil
	}

	if !fs.ValidPath(name) {
		return "", ctxerrors.Wrap(fs.ErrInvalid, "invalid virtual path")
	}

	return name, nil
}

// WalkDir visits paths in lexical order without following directory symlinks.
func WalkDir(reader Reader, root string, visit fs.WalkDirFunc) error {
	info, err := reader.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}

	err = walk(reader, root, fs.FileInfoToDirEntry(info), visit)
	if errors.Is(err, fs.SkipDir) || errors.Is(err, fs.SkipAll) {
		return nil
	}

	return err
}

func walk(
	reader Reader,
	name string,
	entry fs.DirEntry,
	visit fs.WalkDirFunc,
) error {
	if err := visit(name, entry, nil); err != nil {
		if errors.Is(err, fs.SkipDir) && entry.IsDir() {
			return nil
		}

		return err
	}

	if !entry.IsDir() {
		return nil
	}

	entries, err := reader.ReadDir(name)
	if err != nil {
		return visit(name, entry, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, child := range entries {
		if err := walk(
			reader,
			path.Join(name, child.Name()),
			child,
			visit,
		); err != nil {
			if errors.Is(err, fs.SkipDir) {
				return nil
			}

			return err
		}
	}

	return nil
}
