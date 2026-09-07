package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTurnSlots(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		bound    int
		wantNil  bool
		wantSize int
	}{
		{name: "zero takes the default", wantSize: defaultMaxConcurrentTurns},
		{name: "a positive bound is used", bound: 3, wantSize: 3},
		{name: "a negative bound disables it", bound: -1, wantNil: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			slots := newTurnSlots(tc.bound)
			if tc.wantNil {
				assert.Nil(t, slots)

				return
			}

			require.NotNil(t, slots)
			assert.Equal(t, tc.wantSize, cap(slots))
		})
	}
}

// The bound is what stops one process from running unbounded turns at once. A
// caller that will not wait for a slot cancels, which is the only way out.
func TestAcquireTurnSlotBoundsConcurrentTurns(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{turnSlots: make(chan struct{}, 1)}

	release, err := runtime.acquireTurnSlot(context.Background())
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = runtime.acquireTurnSlot(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	release()

	second, err := runtime.acquireTurnSlot(context.Background())
	require.NoError(t, err)
	second()
}

// An embedding caller may run without the bound, and must not deadlock.
func TestAcquireTurnSlotWithoutABoundNeverWaits(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{}

	release, err := runtime.acquireTurnSlot(context.Background())
	require.NoError(t, err)
	release()
}
