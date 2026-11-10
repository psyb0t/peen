package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
)

// controller runs `peen run` as a child process and can kill and restart
// it. Its output is appended to the runtime log across restarts.
type controller struct {
	binary  string
	logPath string
	client  *controlclient.Client
	record  *recorder

	mu      sync.Mutex
	command *exec.Cmd
	exited  chan struct{}
}

func newController(
	binary, logPath string,
	client *controlclient.Client,
	record *recorder,
) *controller {
	return &controller{
		binary:  binary,
		logPath: logPath,
		client:  client,
		record:  record,
	}
}

// start launches the controller and waits until it answers /ready.
func (c *controller) start(ctx context.Context) error {
	logFile, err := os.OpenFile(
		c.logPath,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		outputFileMode,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "open the runtime log")
	}

	// The controller outlives any one step, so it must not stop when a
	// step's context ends. Stop and kill end it explicitly.
	command := exec.CommandContext( //nolint:gosec // Fixed binary path.
		context.WithoutCancel(ctx),
		c.binary,
		serveCommand,
	)
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = os.Environ()

	if err := command.Start(); err != nil {
		_ = logFile.Close() // The start error is the one to report.

		return ctxerrors.Wrap(err, "start the controller")
	}

	exited := make(chan struct{})

	go func() {
		defer close(exited)
		defer func() {
			_ = logFile.Close() // The process has exited; nothing to flush.
		}()

		// Its exit status is expected to be a kill signal here; the
		// driver log records kills and restarts on its own.
		_ = command.Wait()
	}()

	c.mu.Lock()
	c.command = command
	c.exited = exited
	c.mu.Unlock()

	c.record.event(ctx, "controller.started", map[string]any{
		fieldPID: command.Process.Pid,
	})

	return c.awaitReady(ctx)
}

func (c *controller) awaitReady(ctx context.Context) error {
	deadline := time.Now().Add(readyTimeout)

	for time.Now().Before(deadline) {
		if c.client.Reachable(ctx) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for the controller")
		case <-time.After(readyPollInterval):
		}
	}

	return ctxerrors.Wrap(commerr.ErrTimeout, "controller never became ready")
}

func (c *controller) pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.command == nil || c.command.Process == nil {
		return 0
	}

	return c.command.Process.Pid
}

// isRunning reports whether the controller process is still alive.
func (c *controller) isRunning() bool {
	c.mu.Lock()
	exited := c.exited
	c.mu.Unlock()

	if exited == nil {
		return false
	}

	select {
	case <-exited:
		return false
	default:
		return true
	}
}

// killController sends SIGKILL to the controller and waits for it to exit.
func (c *controller) killController(ctx context.Context) error {
	c.mu.Lock()
	command, exited := c.command, c.exited
	c.mu.Unlock()

	if command == nil {
		return ctxerrors.Wrap(commerr.ErrNotFound, "no controller running")
	}

	if err := command.Process.Signal(syscall.SIGKILL); err != nil {
		return ctxerrors.Wrap(err, "kill the controller")
	}

	<-exited

	c.record.event(ctx, "controller.killed", map[string]any{
		fieldPID: command.Process.Pid,
	})

	return nil
}

// killWorkers sends SIGKILL to every Peen worker under the controller.
func (c *controller) killWorkers(ctx context.Context) error {
	processes, err := readProcessTable()
	if err != nil {
		return err
	}

	found := workers(processes, c.pid())
	if len(found) == 0 {
		return ctxerrors.Wrap(commerr.ErrNotFound, "no worker running")
	}

	for _, worker := range found {
		err := syscall.Kill(worker.PID, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return ctxerrors.Wrapf(err, "kill worker %d", worker.PID)
		}

		c.record.event(ctx, "worker.killed", map[string]any{
			fieldPID: worker.PID,
			"args":   worker.Args,
		})
	}

	return nil
}

func (c *controller) kill(ctx context.Context, target killTarget) error {
	if target == killTargetController {
		return c.killController(ctx)
	}

	return c.killWorkers(ctx)
}

// stop asks the controller to shut down and waits, then kills it if it
// does not exit within the grace period.
func (c *controller) stop(ctx context.Context) error {
	c.mu.Lock()
	command, exited := c.command, c.exited
	c.mu.Unlock()

	if command == nil {
		return nil
	}

	select {
	case <-exited:
		return nil
	default:
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		return ctxerrors.Wrap(err, "stop the controller")
	}

	select {
	case <-exited:
		c.record.event(ctx, "controller.stopped", nil)

		return nil
	case <-time.After(stopGrace):
	}

	return c.killController(ctx)
}
