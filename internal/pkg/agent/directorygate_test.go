package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectoryGateAllowsReadersAndExcludesHops(t *testing.T) {
	t.Parallel()
	var gate directoryGate
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	first, err := gate.acquire(ctx, false)
	require.NoError(t, err)
	second, err := gate.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 2, gate.readers)
	first()
	first()
	assert.Equal(t, 1, gate.readers)
	second()
	hop, err := gate.acquire(ctx, true)
	require.NoError(t, err)
	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err = gate.acquire(cancelled, false)
	require.ErrorIs(t, err, context.Canceled)
	hop()
	again, err := gate.acquire(ctx, false)
	require.NoError(t, err)
	again()
}

func TestDirectoryGateCancelledWriterDoesNotStrandReaders(t *testing.T) {
	t.Parallel()
	var gate directoryGate
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	reader, err := gate.acquire(ctx, false)
	require.NoError(t, err)
	writerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		release, acquireErr := gate.acquire(writerCtx, true)
		if release != nil {
			release()
		}
		done <- acquireErr
	}()
	require.Eventually(t, func() bool {
		gate.mutex.Lock()
		defer gate.mutex.Unlock()

		return gate.waitingWriters == 1
	}, time.Second, time.Millisecond)
	stop()
	require.ErrorIs(t, <-done, context.Canceled)
	second, err := gate.acquire(ctx, false)
	require.NoError(t, err)
	second()
	reader()
	hop, err := gate.acquire(ctx, true)
	require.NoError(t, err)
	hop()
}
