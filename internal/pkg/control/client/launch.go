package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	// controlLockFile serializes the start attempt. Two clients that find no
	// controller must not both start one.
	controlLockFile = "control.lock"
	controlLockMode = 0o600

	// controlStateMode keeps the control state directory private to its owner.
	// The lock sits beside the database, so it inherits the same audience.
	controlStateMode = 0o750

	// runCommand is the persistent control-service command. A client starts
	// the same command an operator would, so there is never a second
	// supervisor: `peen run` is the supervisor.
	runCommand = "run"

	readinessPollInterval = 50 * time.Millisecond
)

// LaunchOptions configures the discovery and auto-start sequence.
type LaunchOptions struct {
	// StateDirectory holds the controller's start lock. It is PEEN_STATE_DIR,
	// never the configuration directory visible to every worker.
	StateDirectory string

	// ReadyTimeout bounds the wait for a started controller to answer.
	ReadyTimeout time.Duration

	// Executable overrides the binary a start runs. Empty uses this process's
	// own executable, which is how `peen session open` starts `peen run`.
	Executable string

	// StartController overrides process launch. Tests supply their own.
	StartController func(ctx context.Context, executable string) error
}

// EnsureRunning connects to the local controller, starting one when none is
// listening.
//
// The sequence is the documented one, and the lock is what makes it safe:
//
//  1. try the configured endpoint;
//  2. take the start lock;
//  3. re-check the endpoint while holding it;
//  4. start the controller only if it is still absent;
//  5. wait for readiness.
//
// Step 3 is why two clients racing to start a controller produce one: the
// loser re-checks after the winner has already started it.
func (c *Client) EnsureRunning(
	ctx context.Context,
	options LaunchOptions,
) error {
	if c.Reachable(ctx) {
		return nil
	}

	if options.StateDirectory == "" {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"control state directory",
		)
	}

	release, err := acquireStartLock(options.StateDirectory)
	if err != nil {
		return err
	}

	defer release(ctx)

	// Re-check under the lock. Another client may have started the controller
	// between the first probe and this one.
	if c.Reachable(ctx) {
		return nil
	}

	executable := options.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return ctxerrors.Wrap(err, "resolve the peen executable")
		}
	}

	start := options.StartController
	if start == nil {
		start = startControllerProcess
	}

	ctxscope.GetLogger(ctx).Info(
		"starting the local controller",
		"reason", "no_controller_listening",
	)

	if err := start(ctx, executable); err != nil {
		return ctxerrors.Wrap(err, "start the local controller")
	}

	return c.awaitReady(ctx, options.ReadyTimeout)
}

// awaitReady polls until the controller answers or the deadline passes.
func (c *Client) awaitReady(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"control readiness timeout must be positive",
		)
	}

	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(readinessPollInterval)
	defer ticker.Stop()

	for {
		if c.Reachable(readyCtx) {
			return nil
		}

		select {
		case <-ticker.C:
		case <-readyCtx.Done():
			return ctxerrors.Wrap(
				readyCtx.Err(),
				"wait for the local controller to become ready",
			)
		}
	}
}

// startControllerProcess launches `peen run` detached from this command, so
// the controller outlives the CLI invocation that started it.
func startControllerProcess(_ context.Context, executable string) error {
	// The command deliberately does not inherit this process's context: a
	// controller must not die when the command that started it returns.
	command := exec.Command(executable, runCommand) //nolint:noctx // See above.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := command.Start(); err != nil {
		return ctxerrors.Wrap(err, "launch the controller process")
	}

	// Release the child without waiting for it. The controller owns its own
	// lifetime from here.
	if err := command.Process.Release(); err != nil {
		return ctxerrors.Wrap(err, "release the controller process")
	}

	return nil
}

// acquireStartLock takes an exclusive advisory lock on the control lock file
// and returns the release function.
func acquireStartLock(
	stateDirectory string,
) (func(context.Context), error) {
	if err := os.MkdirAll(stateDirectory, controlStateMode); err != nil {
		return nil, ctxerrors.Wrap(err, "create the control state directory")
	}

	path := filepath.Join(stateDirectory, controlLockFile)

	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_RDWR,
		controlLockMode,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the control start lock")
	}

	if err := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX,
	); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, ctxerrors.Wrap(
				closeErr,
				"close the control start lock after a failed lock",
			)
		}

		return nil, ctxerrors.Wrap(err, "take the control start lock")
	}

	return func(ctx context.Context) {
		logger := ctxscope.GetLogger(ctx)

		if err := syscall.Flock(
			int(file.Fd()),
			syscall.LOCK_UN,
		); err != nil {
			logger.Warn("releasing the control start lock failed", "err", err)
		}

		if err := file.Close(); err != nil {
			logger.Warn("closing the control start lock failed", "err", err)
		}
	}, nil
}
