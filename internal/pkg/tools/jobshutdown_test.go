package tools

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/commander"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/require"
)

type heldStopRunner struct {
	commander.Commander
	stopping chan struct{}
	release  chan struct{}
	once     sync.Once
}

type heldStopProcess struct {
	commander.Process
	runner *heldStopRunner
}

//nolint:ireturn // commander.Commander fixes this process-returning signature.
func (r *heldStopRunner) Start(
	ctx context.Context,
	name string,
	args []string,
	options ...commander.Option,
) (commander.Process, error) {
	process, err := r.Commander.Start(ctx, name, args, options...)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "start held-stop test process")
	}

	return heldStopProcess{Process: process, runner: r}, nil
}

func (p heldStopProcess) Stop(ctx context.Context) error {
	p.runner.once.Do(func() { close(p.runner.stopping) })
	select {
	case <-p.runner.release:
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "wait for stop gate")
	}
	if err := p.Process.Stop(ctx); err != nil {
		return ctxerrors.Wrap(err, "stop gated process")
	}

	return nil
}

func TestJobShutdownRespectsCallerCancellationAndRetry(t *testing.T) {
	t.Parallel()
	registry := newTestRegistry(t, nil)
	runner := &heldStopRunner{
		Commander: commander.New(), stopping: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry.SetCommandRunner(runner)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(runner.release) }) }
	t.Cleanup(release)
	job, err := registry.Start(context.Background(), StartJobInput{
		Command: "sleep 30", Directory: t.TempDir(), Purpose: jobTestPurpose,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- registry.Shutdown(ctx) }()
	select {
	case <-runner.stopping:
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("stop request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("shutdown ignored cancellation")
	}
	release()
	require.NoError(t, registry.Shutdown(context.Background()))
	require.NotEqual(t, JobStateRunning, job.Snapshot().State)
}
