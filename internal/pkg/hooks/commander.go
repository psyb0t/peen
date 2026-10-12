package hooks

import (
	"bytes"
	"context"
	"errors"
	"runtime/debug"

	"github.com/psyb0t/commander"
	"github.com/psyb0t/ctxerrors"
)

const commandStreamBuffer = 64

// WithCommander uses the host process backend for executable hook actions.
// Output is drained with bounded buffers while the process runs.
func WithCommander(runner commander.Commander) CommandRunner {
	return func(ctx context.Context, input CommandInput) ([]byte, error) {
		stdout := make(chan string, commandStreamBuffer)
		stderr := make(chan string, commandStreamBuffer)

		process, err := runner.Start(ctx, input.Command, input.Args,
			commander.WithDir(input.WorkingDir),
			commander.WithEnv(commandEnvironment(input.Environment)),
			commander.WithStdin(bytes.NewReader(input.Stdin)),
			commander.WithStream(stdout, stderr),
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "start custom hook command")
		}

		wait := make(chan error, 1)

		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					wait <- ctxerrors.Wrapf(
						ErrDenied,
						"hook process wait panicked: %v\n%s",
						recovered, debug.Stack())
				}
			}()

			wait <- process.Wait()
		}()

		output, drainErr := drainCommandOutput(stdout, stderr, input.MaxOutput)

		waitErr := <-wait
		if ctx.Err() != nil {
			waitErr = ctx.Err()
		}

		if err := errors.Join(drainErr, waitErr); err != nil {
			return nil, ctxerrors.Wrap(err, "finish custom hook command")
		}

		return output, nil
	}
}

func drainCommandOutput(
	stdout, stderr <-chan string,
	limit int,
) ([]byte, error) {
	output := newLimitedBuffer(limit)
	diagnostic := newLimitedBuffer(limit)

	var outputErr error

	for stdout != nil || stderr != nil {
		select {
		case line, open := <-stdout:
			if !open {
				stdout = nil

				continue
			}

			if _, err := output.Write([]byte(line + "\n")); err != nil {
				outputErr = err
			}
		case line, open := <-stderr:
			if !open {
				stderr = nil

				continue
			}

			if _, err := diagnostic.Write([]byte(line + "\n")); err != nil {
				outputErr = err
			}
		}
	}

	return output.Bytes(), outputErr
}
