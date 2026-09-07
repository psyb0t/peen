package server

import (
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
)

// ctxerrors appends "[file:line in func]" to every wrapped error, and an
// error's text used to reach the response body verbatim. That published the
// absolute source path, line number, and internal function name of whatever
// failed to any caller who could trigger the error.
func TestClientMessageStripsTheSourceLocation(t *testing.T) {
	t.Parallel()

	t.Run("a wrapped error keeps its chain and loses the location", func(t *testing.T) {
		t.Parallel()

		err := ctxerrors.Wrap(commerr.ErrValidationFailed, "message")

		message := clientMessage(err)
		assert.Contains(t, message, "message")
		assert.Contains(t, message, commerr.ErrValidationFailed.Error())
		assertNoSourceLocation(t, message)
	})

	t.Run("nested wraps lose every location", func(t *testing.T) {
		t.Parallel()

		err := ctxerrors.Wrap(
			ctxerrors.Wrap(commerr.ErrValidationFailed, "inner"),
			"outer",
		)

		message := clientMessage(err)
		assert.Contains(t, message, "outer")
		assertNoSourceLocation(t, message)
	})

	t.Run("a plain error is unchanged", func(t *testing.T) {
		t.Parallel()

		assert.Equal(
			t,
			commerr.ErrValidationFailed.Error(),
			clientMessage(commerr.ErrValidationFailed),
		)
	})

	t.Run("a nil error is empty", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, clientMessage(nil))
	})
}

func assertNoSourceLocation(t *testing.T, message string) {
	t.Helper()

	assert.NotContains(t, message, errorLocationMarker)
	assert.NotContains(t, message, ".go:")
	assert.NotContains(t, message, "/peen/")
}
