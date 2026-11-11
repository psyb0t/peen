package testinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	retryTestImage    = "peen-test-image:retry"
	retryTestAttempts = 3
	retryTestBackoff  = time.Millisecond
)

var errRetryTestBuild = errors.New("build failed")

// failingBuild fails its first failures calls and then returns
// retryTestImage, counting every call in calls.
func failingBuild(
	failures int,
	calls *int,
) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		*calls++
		if *calls <= failures {
			return "", errRetryTestBuild
		}

		return retryTestImage, nil
	}
}

func TestRetryAppImageBuild(t *testing.T) {
	t.Run("first attempt succeeds", func(t *testing.T) {
		calls := 0
		image, err := retryAppImageBuild(t.Context(), retryTestAttempts,
			retryTestBackoff, failingBuild(0, &calls))

		require.NoError(t, err)
		assert.Equal(t, retryTestImage, image)
		assert.Equal(t, 1, calls)
	})

	t.Run("succeeds on the last attempt", func(t *testing.T) {
		calls := 0
		image, err := retryAppImageBuild(t.Context(), retryTestAttempts,
			retryTestBackoff, failingBuild(retryTestAttempts-1, &calls))

		require.NoError(t, err)
		assert.Equal(t, retryTestImage, image)
		assert.Equal(t, retryTestAttempts, calls)
	})

	t.Run("fails after every attempt", func(t *testing.T) {
		calls := 0
		image, err := retryAppImageBuild(t.Context(), retryTestAttempts,
			retryTestBackoff, failingBuild(retryTestAttempts, &calls))

		require.ErrorIs(t, err, errRetryTestBuild)
		assert.Empty(t, image)
		assert.Equal(t, retryTestAttempts, calls)
	})

	t.Run("one attempt means no retry", func(t *testing.T) {
		calls := 0
		_, err := retryAppImageBuild(t.Context(), 1, retryTestBackoff,
			failingBuild(1, &calls))

		require.ErrorIs(t, err, errRetryTestBuild)
		assert.Equal(t, 1, calls)
	})

	t.Run("stops waiting when the context ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		calls := 0
		_, err := retryAppImageBuild(ctx, retryTestAttempts, time.Hour,
			failingBuild(retryTestAttempts, &calls))

		require.ErrorIs(t, err, errRetryTestBuild)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
	})
}
