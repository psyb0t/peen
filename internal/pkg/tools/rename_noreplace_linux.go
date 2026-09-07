//go:build linux

package tools

import (
	"github.com/psyb0t/ctxerrors"
	"golang.org/x/sys/unix"
)

// renameNoReplace atomically renames source only when destination is absent.
func renameNoReplace(source, destination string) error {
	if err := unix.Renameat2(
		unix.AT_FDCWD,
		source,
		unix.AT_FDCWD,
		destination,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return ctxerrors.Wrap(err, "rename without replacement")
	}

	return nil
}
