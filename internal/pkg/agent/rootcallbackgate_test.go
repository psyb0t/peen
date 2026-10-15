package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const rootCallbackGateTestTimeout = 50 * time.Millisecond

func TestRootCallbackGateSharedReadersAndCancelledWriter(t *testing.T) {
	t.Parallel()
	var gate rootCallbackGate
	first, err := gate.acquire(t.Context(), false)
	require.NoError(t, err)
	second, err := gate.acquire(t.Context(), false)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), rootCallbackGateTestTimeout)
	t.Cleanup(cancel)
	_, err = gate.acquire(ctx, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	first()
	first()
	second()
	writer, err := gate.acquire(t.Context(), true)
	require.NoError(t, err)
	writer()
}

func TestRootCallbackGateWriterBlocksReadersUntilCancelled(t *testing.T) {
	t.Parallel()
	var gate rootCallbackGate
	writer, err := gate.acquire(t.Context(), true)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), rootCallbackGateTestTimeout)
	t.Cleanup(cancel)
	_, err = gate.acquire(ctx, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	writer()
	reader, err := gate.acquire(t.Context(), false)
	require.NoError(t, err)
	reader()
}
