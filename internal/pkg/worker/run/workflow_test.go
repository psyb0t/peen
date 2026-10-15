package run

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lifecycleWorkflowWorker struct {
	startErr error
	waitErr  error
	//nolint:containedctx // Capture the supplied lifetime for cancellation checks.
	ctx   context.Context
	waits atomic.Int64
}

func (w *lifecycleWorkflowWorker) Start(ctx context.Context) error {
	w.ctx = ctx

	return w.startErr
}

func (w *lifecycleWorkflowWorker) WaitForCompletion() error {
	w.waits.Add(1)
	if w.ctx.Err() == nil {
		return commerr.ErrInvalidState
	}

	return w.waitErr
}

func TestStartNativeWorkflowCancelsBeforeDrainingOnce(t *testing.T) {
	t.Parallel()
	worker := &lifecycleWorkflowWorker{}
	stop, err := startNativeWorkflow(t.Context(), worker)
	require.NoError(t, err)
	assert.NoError(t, worker.ctx.Err())
	require.NoError(t, stop())
	require.NoError(t, stop())
	assert.ErrorIs(t, worker.ctx.Err(), context.Canceled)
	assert.Equal(t, int64(1), worker.waits.Load())
}

func TestStartNativeWorkflowCancelsFailedStartup(t *testing.T) {
	t.Parallel()
	worker := &lifecycleWorkflowWorker{startErr: commerr.ErrFetchFailed}
	stop, err := startNativeWorkflow(t.Context(), worker)
	require.ErrorIs(t, err, commerr.ErrFetchFailed)
	assert.Nil(t, stop)
	assert.ErrorIs(t, worker.ctx.Err(), context.Canceled)
	assert.Equal(t, int64(1), worker.waits.Load())
}

func TestStartNativeWorkflowRetainsDrainFailure(t *testing.T) {
	t.Parallel()
	worker := &lifecycleWorkflowWorker{waitErr: commerr.ErrFetchFailed}
	stop, err := startNativeWorkflow(t.Context(), worker)
	require.NoError(t, err)
	require.ErrorIs(t, stop(), commerr.ErrFetchFailed)
	require.ErrorIs(t, stop(), commerr.ErrFetchFailed)
	assert.Equal(t, int64(1), worker.waits.Load())
}
