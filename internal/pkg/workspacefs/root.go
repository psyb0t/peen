package workspacefs

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"golang.org/x/sys/unix"
)

const maximumSymlinkDepth = 40

// Root confines every operation through os.Root, including parent traversal.
type Root struct{ Handle *os.Root }

func (r Root) Stat(name string) (fs.FileInfo, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	info, err := r.Handle.Stat(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat rooted path")
	}

	return info, nil
}

func (r Root) Lstat(name string) (fs.FileInfo, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	info, err := r.Handle.Lstat(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "inspect rooted path")
	}

	return info, nil
}

func (r Root) Open(name string) (fs.File, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	file, err := r.Handle.Open(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open rooted path")
	}

	return file, nil
}

func (r Root) ReadDir(name string) ([]fs.DirEntry, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	entries, err := fs.ReadDir(r.Handle.FS(), name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read rooted directory")
	}

	return entries, nil
}

func (r Root) ReadFile(name string) ([]byte, error) {
	name, err := ValidPath(name)
	if err != nil {
		return nil, err
	}

	data, err := r.Handle.ReadFile(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read rooted file")
	}

	return data, nil
}

func (r Root) RealPath(name string) (string, error) {
	name, err := ValidPath(name)
	if err != nil {
		return "", err
	}

	for range maximumSymlinkDepth {
		parts := strings.Split(name, "/")
		found := false

		for index := range parts {
			prefix := strings.Join(parts[:index+1], "/")

			info, err := r.Lstat(prefix)
			if err != nil {
				return "", err
			}

			if info.Mode()&fs.ModeSymlink == 0 {
				continue
			}

			target, err := r.Handle.Readlink(prefix)
			if err != nil {
				return "", ctxerrors.Wrap(err, "read rooted symlink")
			}

			if path.IsAbs(target) {
				return "", ctxerrors.Wrap(
					fs.ErrPermission,
					"absolute symlink target",
				)
			}

			name = path.Join(path.Dir(prefix), target,
				strings.Join(parts[index+1:], "/"))
			if _, err := ValidPath(name); err != nil {
				return "", err
			}

			found = true

			break
		}

		if !found {
			return name, nil
		}
	}

	return "", ctxerrors.Wrap(fs.ErrInvalid, "too many symlinks")
}

func (r Root) MkdirAll(name string, mode fs.FileMode) error {
	name, err := ValidPath(name)
	if err != nil {
		return err
	}

	if err := r.Handle.MkdirAll(name, mode); err != nil {
		return ctxerrors.Wrap(err, "create rooted directory")
	}

	return nil
}

func (r Root) Remove(name string) error {
	name, err := ValidPath(name)
	if err != nil {
		return err
	}

	if err := r.Handle.Remove(name); err != nil {
		return ctxerrors.Wrap(err, "remove rooted path")
	}

	return nil
}

func (r Root) RemoveAll(name string) error {
	name, err := ValidPath(name)
	if err != nil {
		return err
	}

	if name == "." {
		return ctxerrors.Wrap(fs.ErrPermission, "remove rooted filesystem")
	}

	if err := r.Handle.RemoveAll(name); err != nil {
		return ctxerrors.Wrap(err, "remove rooted tree")
	}

	return nil
}

func (r Root) Chmod(name string, mode fs.FileMode) error {
	name, err := ValidPath(name)
	if err != nil {
		return err
	}

	if err := r.Handle.Chmod(name, mode); err != nil {
		return ctxerrors.Wrap(err, "chmod rooted path")
	}

	return nil
}

func (r Root) RenameNoReplace(source, destination string) error {
	if _, err := ValidPath(source); err != nil {
		return err
	}

	if _, err := ValidPath(destination); err != nil {
		return err
	}

	sourceParent, err := r.Handle.Open(path.Dir(source))
	if err != nil {
		return ctxerrors.Wrap(err, "open rooted source parent")
	}

	destinationParent, err := r.Handle.Open(path.Dir(destination))
	if err != nil {
		return errors.Join(ctxerrors.Wrap(
			err,
			"open rooted destination parent",
		), sourceParent.Close())
	}
	// Parent descriptors are opened through os.Root. renameat2 therefore
	// cannot follow a swapped ancestor outside the jail or overwrite a target.
	renameErr := unix.Renameat2(
		int(sourceParent.Fd()), path.Base(source),
		int(destinationParent.Fd()), path.Base(destination),
		unix.RENAME_NOREPLACE,
	)
	if err := errors.Join(
		renameErr,
		sourceParent.Close(),
		destinationParent.Close(),
	); err != nil {
		return ctxerrors.Wrap(err, "rename rooted path exclusively")
	}

	return nil
}

func (r Root) CreateExclusive(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	return r.writeAtomic(name, data, mode, true)
}

func (r Root) WriteFileAtomic(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	return r.writeAtomic(name, data, mode, false)
}

func (r Root) writeAtomic(
	name string,
	data []byte,
	mode fs.FileMode,
	exclusive bool,
) error {
	name, err := ValidPath(name)
	if err != nil {
		return err
	}

	if !exclusive {
		resolved, resolveErr := r.RealPath(name)
		if resolveErr == nil {
			name = resolved
		}

		if resolveErr != nil && !errors.Is(resolveErr, fs.ErrNotExist) {
			return resolveErr
		}
	}

	temporary := path.Join(path.Dir(name), temporaryPrefix+uuid.NewString())

	file, err := r.Handle.OpenFile(
		temporary,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		mode,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "create rooted atomic temporary")
	}

	writeErr := finishFile(file, data, mode)
	if writeErr == nil {
		if exclusive {
			writeErr = r.RenameNoReplace(temporary, name)
		} else {
			writeErr = r.Handle.Rename(temporary, name)
		}
	}

	if writeErr != nil {
		return errors.Join(ctxerrors.Wrap(
			writeErr,
			"publish rooted atomic file",
		),
			r.Remove(temporary))
	}

	return nil
}
