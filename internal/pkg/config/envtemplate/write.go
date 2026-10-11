package envtemplate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	// FileMode keeps the env file private to its owner: it holds the API
	// token and provider keys once an operator fills them in.
	FileMode fs.FileMode = 0o600

	temporaryFilePattern = ".env-*.tmp"
)

// WriteNew renders values and writes them to path, which must be absolute and
// must not exist yet.
//
// The file is written in full to a temporary file beside path and then hard
// linked into place. A link never replaces an existing name, so an operator's
// .env is never overwritten, and a reader never sees a partly written file.
// An existing path returns commerr.ErrAlreadyExists and is left unchanged.
// Invalid values return commerr.ErrValidationFailed and write nothing.
func WriteNew(ctx context.Context, path string, values Values) error {
	if strings.ContainsAny(path, lineBreakCharacters) ||
		!filepath.IsAbs(path) {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"env file path must be an absolute path on one line, got %q",
			path,
		)
	}

	rendered, err := Render(values)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(path); err == nil {
		return alreadyExists(path)
	}

	temporaryPath, err := writeTemporary(filepath.Dir(path), rendered)
	if err != nil {
		return err
	}

	// The temporary name is removed on every path. After a successful link
	// the content stays reachable through path.
	defer func() {
		if err := os.Remove(temporaryPath); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			ctxscope.GetLogger(ctx).Warn(
				"temporary env file removal failed",
				"path", temporaryPath,
				"reason", "cleanup",
				"err", err,
			)
		}
	}()

	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return alreadyExists(path)
		}

		return ctxerrors.Wrapf(err, "link the env file into place at %s", path)
	}

	return nil
}

func alreadyExists(path string) error {
	return ctxerrors.Wrapf(
		commerr.ErrAlreadyExists,
		"%s already exists and was left unchanged",
		path,
	)
}

// writeTemporary writes content to a new private file in directory and
// returns its path. The file is synced before it is returned, so the linked
// file is complete even after a crash.
func writeTemporary(directory string, content []byte) (string, error) {
	file, err := os.CreateTemp(directory, temporaryFilePattern)
	if err != nil {
		return "", ctxerrors.Wrapf(
			err,
			"create a temporary env file in %s",
			directory,
		)
	}

	temporaryPath := file.Name()

	writeErr := writeAndSync(file, content)
	closeErr := file.Close()

	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", errors.Join(
			ctxerrors.Wrap(err, "write the temporary env file"),
			removeTemporary(temporaryPath),
		)
	}

	return temporaryPath, nil
}

func writeAndSync(file *os.File, content []byte) error {
	// CreateTemp already uses 0600. Setting it again states the contract
	// instead of relying on the library default.
	if err := file.Chmod(FileMode); err != nil {
		return ctxerrors.Wrap(err, "set the env file mode")
	}

	if _, err := file.Write(content); err != nil {
		return ctxerrors.Wrap(err, "write the env file content")
	}

	if err := file.Sync(); err != nil {
		return ctxerrors.Wrap(err, "sync the env file")
	}

	return nil
}

func removeTemporary(path string) error {
	if err := os.Remove(path); err != nil {
		return ctxerrors.Wrap(err, "remove the temporary env file")
	}

	return nil
}
