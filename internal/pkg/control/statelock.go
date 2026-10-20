package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	// stateLockFile marks the state directory as owned by one running
	// controller. It is separate from the client's start lock, which a client
	// holds only while it starts a controller.
	stateLockFile = "controller.lock"
	stateLockMode = 0o600

	// stateDirectoryMode keeps the state directory private to its owner, the
	// same mode the client's start lock creates it with.
	stateDirectoryMode = 0o750
)

// LockStateDirectory takes the state directory for one controller and returns
// the function that gives it back.
//
// SQLite alone does not stop a second controller from opening the same
// databases, and a second controller's startup recovery would mark the first
// one's running turns as interrupted. The lock is an advisory flock, so the
// kernel releases it when the process ends, crash included. It fails with
// commerr.ErrLockHeld while another controller holds the directory.
func LockStateDirectory(
	stateDirectory string,
) (func(context.Context), error) {
	if err := os.MkdirAll(stateDirectory, stateDirectoryMode); err != nil {
		return nil, ctxerrors.Wrap(err, "create the state directory")
	}

	path := filepath.Join(stateDirectory, stateLockFile)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, stateLockMode)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the state directory lock")
	}

	if err := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		lockErr := ctxerrors.Wrap(err, "take the state directory lock")
		if errors.Is(err, syscall.EWOULDBLOCK) {
			lockErr = ctxerrors.Wrapf(
				commerr.ErrLockHeld,
				"another controller is using the state directory %s",
				stateDirectory,
			)
		}

		return nil, errors.Join(lockErr, file.Close())
	}

	return func(ctx context.Context) {
		logger := ctxscope.GetLogger(ctx)

		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
			logger.Warn("releasing the state directory lock failed", "err", err)
		}

		if err := file.Close(); err != nil {
			logger.Warn("closing the state directory lock failed", "err", err)
		}
	}, nil
}
