package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	binarySniffBytes    = 8000
	newFileMode         = fs.FileMode(0o644)
	newDirectoryMode    = fs.FileMode(0o755)
	temporaryFilePrefix = ".peen-tool-"
)

// hashBytes returns the lowercase hexadecimal SHA-256 of the content.
func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

// entryTypeOf classifies a directory entry without following symlinks.
func entryTypeOf(mode fs.FileMode) EntryType {
	switch {
	case mode&fs.ModeSymlink != 0:
		return EntryTypeSymlink
	case mode.IsDir():
		return EntryTypeDirectory
	case mode.IsRegular():
		return EntryTypeFile
	default:
		return EntryTypeOther
	}
}

// isBinary reports content that text tools refuse. A NUL byte or invalid
// UTF-8 in the leading sniff window is treated as binary.
func isBinary(content []byte) bool {
	window := content
	if len(window) > binarySniffBytes {
		window = window[:binarySniffBytes]
	}

	if bytes.IndexByte(window, 0) >= 0 {
		return true
	}

	return !utf8.Valid(trimPartialRune(window, len(content)))
}

// trimPartialRune drops a multi-byte rune cut by the sniff window so a valid
// file is not misread as binary at the boundary.
func trimPartialRune(window []byte, total int) []byte {
	if len(window) == total {
		return window
	}

	for end := len(window); end > 0 && end > len(window)-utf8.UTFMax; end-- {
		if utf8.Valid(window[:end]) {
			return window[:end]
		}
	}

	return window
}

// readRegularFile reads one regular file whole, rejecting anything larger
// than maxBytes so a tool cannot be used to pull an unbounded file into
// memory.
func readRegularFile(path string, maxBytes int64) ([]byte, fs.FileMode, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, wrapPathError(err, "open file")
	}

	defer closeQuietly(file)

	info, err := file.Stat()
	if err != nil {
		return nil, 0, wrapPathError(err, "stat open file")
	}

	if !info.Mode().IsRegular() {
		return nil, 0, ctxerrors.Wrap(ErrNotRegularFile, path)
	}

	if info.Size() > maxBytes {
		return nil, 0, ctxerrors.Wrap(
			ErrLimitExceeded,
			"file exceeds read bound",
		)
	}

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, wrapPathError(err, "read file")
	}

	return content, info.Mode().Perm(), nil
}

// writeFileAtomic replaces path through a same-directory temporary file,
// fsync, and rename so an interrupted write never leaves partial content in
// place. The caller supplies the mode for a newly created file; an existing
// file keeps the mode it already has.
func writeFileAtomic(path string, content []byte, mode fs.FileMode) error {
	path = resolveWriteTarget(path)
	directory := filepath.Dir(path)

	temporary, err := os.CreateTemp(directory, temporaryFilePrefix)
	if err != nil {
		return wrapPathError(err, "create temporary file")
	}

	temporaryPath := temporary.Name()

	if err := finishTemporaryFile(temporary, content, mode); err != nil {
		removeQuietly(temporaryPath)

		return err
	}

	if err := os.Rename(temporaryPath, path); err != nil {
		removeQuietly(temporaryPath)

		return wrapPathError(err, "rename temporary file")
	}

	syncDirectory(directory)

	return nil
}

// writeNewFileAtomic publishes a fully synced temporary file only if path is
// still absent. The final rename cannot replace a destination created after an
// earlier existence check.
func writeNewFileAtomic(path string, content []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)

	temporary, err := os.CreateTemp(directory, temporaryFilePrefix)
	if err != nil {
		return wrapPathError(err, "create temporary file")
	}

	temporaryPath := temporary.Name()
	if err := finishTemporaryFile(temporary, content, mode); err != nil {
		removeQuietly(temporaryPath)

		return err
	}

	if err := renameNoReplace(temporaryPath, path); err != nil {
		removeQuietly(temporaryPath)

		return wrapPathError(err, "publish new file")
	}

	syncDirectory(directory)

	return nil
}

// resolveWriteTarget follows a symlink so an atomic replacement lands on the
// file the link points at. Renaming onto the link itself would silently turn
// it into a regular file, which is not what writing to that path normally
// does. A path that does not resolve is used as given.
func resolveWriteTarget(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}

	return resolved
}

func finishTemporaryFile(
	file *os.File,
	content []byte,
	mode fs.FileMode,
) error {
	defer closeQuietly(file)

	if _, err := file.Write(content); err != nil {
		return wrapPathError(err, "write temporary file")
	}

	if err := file.Sync(); err != nil {
		return wrapPathError(err, "sync temporary file")
	}

	if err := file.Chmod(mode); err != nil {
		return wrapPathError(err, "set temporary file mode")
	}

	return nil
}

// syncDirectory flushes the rename so the replacement survives a crash. It is
// deliberately best effort and reports nothing: the data is already durable
// from the temporary file's own Sync, some platforms and filesystems refuse to
// open a directory for sync at all, and failing the whole write over a missing
// metadata flush would be worse than the crash window it closes.
func syncDirectory(directory string) {
	handle, err := os.Open(directory)
	if err != nil {
		return
	}

	defer closeQuietly(handle)

	// Discarded: see the best-effort contract above.
	_ = handle.Sync()
}

// closeQuietly discards the close error because every caller is on a read path
// that already reports the read result, where a failed close adds nothing.
func closeQuietly(file *os.File) {
	_ = file.Close()
}

// removeQuietly discards the removal error because it only ever cleans up a
// temporary file whose write already failed, and that failure is what the
// caller returns.
func removeQuietly(path string) {
	_ = os.Remove(path)
}

// wrapPathError maps filesystem errors onto the shared sentinels so callers
// can branch with errors.Is instead of matching operating-system strings.
func wrapPathError(err error, message string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ctxerrors.Wrap(commerr.ErrNotFound, message)
	case errors.Is(err, fs.ErrExist):
		return ctxerrors.Wrap(commerr.ErrAlreadyExists, message)
	case errors.Is(err, fs.ErrPermission):
		return ctxerrors.Wrap(commerr.ErrPermissionDenied, message)
	default:
		return ctxerrors.Wrap(err, message)
	}
}
