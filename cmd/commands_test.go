package main

import (
	"context"
	"io"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// launchInputRecorder stands in for the worker's stdin and records whether the
// command read the launch document from it.
type launchInputRecorder struct {
	hasBeenRead bool
}

func (r *launchInputRecorder) Read([]byte) (int, error) {
	r.hasBeenRead = true

	return 0, io.EOF
}

// The worker must not run unprotected: when it cannot mark itself
// non-dumpable it fails before it reads the launch document, which carries the
// worker credential.
func TestWorkerCommandFailsBeforeReadingTheLaunchWhenItCannotBecomeNonDumpable(
	t *testing.T,
) {
	t.Parallel()

	launchInput := &launchInputRecorder{}

	command := workerCommand(workerCommandDependencies{
		enableNonDumpable: func(context.Context) error {
			return ctxerrors.Wrap(
				commerr.ErrPermissionDenied,
				"refuse the non-dumpable flag",
			)
		},
	})
	command.SetIn(launchInput)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{})

	err := command.ExecuteContext(t.Context())

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.False(t, launchInput.hasBeenRead, "the launch document was read")
}
