// Package native launches a session worker as a child process of the
// controller.
//
// The child is the same Peen binary running `peen worker`, started in the
// session workspace under the controller's own process identity. Its launch
// document arrives on stdin, so the credential never appears in the process
// table or on disk.
package native

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
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/workerlog"
)

const (
	// terminateGrace is how long a worker gets to exit after the signal
	// before it is killed outright.
	terminateGrace = 10 * time.Second
)

// Options builds a native launcher.
type Options struct {
	// BinaryPath is the Peen binary to run. Empty means this process's own
	// executable, which is what makes a native worker the same build as its
	// controller.
	BinaryPath string

	// Environment is the base environment every worker inherits. Empty means
	// the controller's own.
	Environment []string
}

// Launcher starts native workers.
type Launcher struct {
	binaryPath  string
	environment []string
}

// New resolves the binary a native worker runs.
func New(options Options) (*Launcher, error) {
	binaryPath := options.BinaryPath

	if binaryPath == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil, ctxerrors.Wrap(
				err,
				"resolve the controller executable for native workers",
			)
		}

		binaryPath = resolved
	}

	environment := options.Environment
	if environment == nil {
		environment = os.Environ()
	}

	return &Launcher{
		binaryPath:  binaryPath,
		environment: environment,
	}, nil
}

// Kind reports the profile kind this launcher serves.
func (l *Launcher) Kind() worker.Kind {
	return worker.KindNative
}

// Launch starts one `peen worker` child in the session workspace.
//
//nolint:ireturn // The Launcher contract is returning a Process.
func (l *Launcher) Launch(
	ctx context.Context,
	request worker.LaunchRequest,
) (worker.Process, error) {
	if request.Profile.Kind != worker.KindNative {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"profile %q is not a native profile",
			request.Profile.Name,
		)
	}

	document, err := request.Document.Encode()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode the worker launch document")
	}

	stdin, err := documentReader(document)
	if err != nil {
		return nil, err
	}

	// The worker outlives this call, so it is deliberately not bound to the
	// launch context: CommandContext would kill it the moment the caller's
	// context ended. Stop ends it instead. The binary is the controller's own
	// executable, never client input.
	//nolint:gosec,noctx // Own executable; the worker outlives the launch ctx.
	command := exec.Command(l.binaryPath, worker.WorkerCommand)
	command.Dir = request.Document.Workspace
	command.Env = l.environment
	command.Stdin = stdin

	// The worker's records go through this process's logger rather than
	// straight to its file descriptors. Writing to os.Stdout directly skipped
	// every configured sink, so the audit trail lost the hook, skill, child
	// agent, and tool records the agent loop produces.
	//
	// The pipe is created here rather than with StdoutPipe because that one is
	// closed by Wait, which would cut the relay off mid-drain when the worker
	// exits.
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the worker output pipe")
	}

	command.Stdout = outputWriter
	command.Stderr = outputWriter

	// A process group lets a stop reach the whole worker tree rather than only
	// the worker itself, which matters because a worker starts job processes.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := command.Start(); err != nil {
		closeWorkerPipe(outputReader, outputWriter)

		return nil, ctxerrors.Wrap(err, "start the native worker process")
	}

	startWorkerLogRelay(ctx, outputReader, outputWriter, request.Document)

	ctxscope.GetLogger(ctx).Info(
		"native worker started",
		"session_id", request.Document.SessionID.String(),
		"generation_id", request.Document.GenerationID.String(),
		"workspace", request.Document.Workspace,
		"pid", command.Process.Pid,
	)

	return newProcess(command), nil
}

// startWorkerLogRelay hands the worker's output to this process's logger.
//
// The parent's copy of the write end is closed first. The child holds its own,
// so that close is what lets the relay see EOF when the worker exits.
func startWorkerLogRelay(
	ctx context.Context,
	reader, writer *os.File,
	document worker.LaunchDocument,
) {
	if err := writer.Close(); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"closing the worker output writer failed",
			"err", err,
		)
	}

	go func() {
		defer func() {
			if err := reader.Close(); err != nil {
				ctxscope.GetLogger(ctx).Debug(
					"closing the worker output reader failed",
					"err", err,
				)
			}
		}()

		workerlog.Relay(
			context.WithoutCancel(ctx),
			reader,
			document.SessionID,
			document.GenerationID,
		)
	}()
}

// closeWorkerPipe releases both ends of the output pipe when the worker never
// started, so a failed launch does not leak descriptors.
func closeWorkerPipe(reader, writer *os.File) {
	_ = reader.Close()
	_ = writer.Close()
}

// documentReader hands the launch document to the child on stdin. A pipe keeps
// the credential out of the process table and off disk.
func documentReader(document []byte) (*os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the worker launch pipe")
	}

	go func() {
		defer func() {
			if err := writer.Close(); err != nil {
				return
			}
		}()

		if _, err := writer.Write(document); err != nil {
			return
		}
	}()

	return reader, nil
}

// process is one running native worker.
type process struct {
	command *exec.Cmd

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

func newProcess(command *exec.Cmd) *process {
	return &process{command: command}
}

// Describe reports the worker's operating system identity.
func (p *process) Describe() worker.Descriptor {
	if p.command.Process == nil {
		return worker.Descriptor{}
	}

	return worker.Descriptor{ProcessID: p.command.Process.Pid}
}

// Stop signals the worker's process group, then kills it if the grace period
// passes. Signalling the group rather than the process reaches the commands
// the worker started.
func (p *process) Stop(ctx context.Context) error {
	if p.command.Process == nil {
		return nil
	}

	if err := p.signalGroup(syscall.SIGTERM); err != nil {
		return err
	}

	exited := make(chan struct{})

	go func() {
		defer close(exited)

		p.wait()
	}()

	grace := time.NewTimer(terminateGrace)
	defer grace.Stop()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
	case <-grace.C:
	}

	if err := p.signalGroup(syscall.SIGKILL); err != nil {
		return err
	}

	<-exited

	return nil
}

func (p *process) signalGroup(signal syscall.Signal) error {
	// A negative PID addresses the process group the child leads.
	if err := syscall.Kill(-p.command.Process.Pid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return ctxerrors.Wrapf(
			err,
			"signal the native worker process group with %s",
			signal,
		)
	}

	return nil
}

// Wait blocks until the worker ends and reports its exit code.
func (p *process) Wait() (int, error) {
	p.wait()

	return p.exitCode, p.waitErr
}

func (p *process) wait() {
	p.waitOnce.Do(func() {
		err := p.command.Wait()
		if err == nil {
			p.exitCode = p.command.ProcessState.ExitCode()

			return
		}

		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			p.exitCode = exitErr.ExitCode()

			return
		}

		p.waitErr = ctxerrors.Wrap(err, "wait for the native worker")
	})
}
